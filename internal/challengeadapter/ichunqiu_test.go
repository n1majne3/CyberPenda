package challengeadapter_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"pentest/internal/challengeadapter"
)

const ichunqiuListBody = `{
	"code": 0,
	"message": "查询成功",
	"data": [
		{
			"question_id": "q-pwn-1",
			"title": "sign_shellcode",
			"score": 500,
			"real_score": 500,
			"file_url": "https://files.test/attach.zip",
			"is_solved": false,
			"solved_number": 0,
			"category": "pwn",
			"attributes": ["docker"],
			"description": "test",
			"interactive": "true",
			"capabilities": ["docker"],
			"connection": {
				"docker_url": "nc 10.1.2.3 9999",
				"docker_ip": "10.1.2.3",
				"docker_port": "9999"
			},
			"extensions": {"pwn": "<Pwn扩展信息>"}
		},
		{
			"question_id": "q-misc-1",
			"title": "测试_附件",
			"score": 100,
			"real_score": 100,
			"file_url": "",
			"is_solved": true,
			"solved_number": 3,
			"category": "misc",
			"description": "测试_多附件题目",
			"interactive": "false",
			"capabilities": ["能力"],
			"connection": [],
			"extensions": {"aaa": "<Misc扩展信息>"}
		}
	]
}`

func mustIchunqiu(t *testing.T) challengeadapter.Manifest {
	t.Helper()
	manifest, err := challengeadapter.Load("ichunqiu", nil)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestLoadBuiltinIchunqiuManifest(t *testing.T) {
	manifest := mustIchunqiu(t)
	if manifest.TokenQuery != "token" {
		t.Fatalf("token_query = %q", manifest.TokenQuery)
	}
	if manifest.BaseURLEnv != "ICHUNQIU_BASE_URL" || manifest.TokenEnv != "ICHUNQIU_TOKEN" {
		t.Fatalf("env names = %q %q", manifest.BaseURLEnv, manifest.TokenEnv)
	}
	operations := manifest.Operations
	if operations["list"].Method != http.MethodGet || operations["list"].Path == "" {
		t.Fatalf("list operation = %#v", operations["list"])
	}
	if operations["submit"].Query["answer"] != "{{candidate}}" {
		t.Fatalf("submit operation = %#v", operations["submit"])
	}
	if manifest.ChallengeFields["unique_code"] != "question_id" {
		t.Fatalf("challenge_fields = %#v", manifest.ChallengeFields)
	}
	if manifest.SubmitCorrect == "" {
		t.Fatal("submit_correct is required")
	}
}

func TestIchunqiuListInjectsTokenQueryAndMapsFields(t *testing.T) {
	var gotPath, gotQuery, gotAuthorization string
	client := challengeadapter.NewHTTPDriver(challengeadapter.HTTPDriverConfig{
		Manifest: mustIchunqiu(t),
		BaseURL:  "http://terminator.test",
		Token:    "team-secret",
		Client: &http.Client{Transport: roundTrip(func(request *http.Request) *http.Response {
			gotPath = request.URL.Path
			gotQuery = request.URL.RawQuery
			gotAuthorization = request.Header.Get("Authorization")
			return jsonResponse(ichunqiuListBody)
		})},
		Timeout: time.Second,
	})
	result, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/04cb510e425bd8f64fa97ba66f3935e1" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "token=team-secret") {
		t.Fatalf("query = %q", gotQuery)
	}
	if gotAuthorization != "" {
		t.Fatalf("authorization header = %q, expected none", gotAuthorization)
	}
	if len(result.Challenges) != 2 {
		t.Fatalf("challenges = %#v", result.Challenges)
	}
	container := result.Challenges[0]
	if container.UniqueCode != "q-pwn-1" || container.IsCompleted {
		t.Fatalf("container = %#v", container)
	}
	if container.TotalScore != 500 || container.Category != "pwn" {
		t.Fatalf("container = %#v", container)
	}
	if len(container.ContainerAddr) != 1 || container.ContainerAddr[0] != "nc 10.1.2.3 9999" {
		t.Fatalf("container_addr = %#v", container.ContainerAddr)
	}
	if container.FileURL != "https://files.test/attach.zip" {
		t.Fatalf("file_url = %q", container.FileURL)
	}
	if !containsString(container.Capabilities, "docker") {
		t.Fatalf("capabilities = %#v", container.Capabilities)
	}
	static := result.Challenges[1]
	if !static.IsCompleted || static.UniqueCode != "q-misc-1" {
		t.Fatalf("static = %#v", static)
	}
	if len(static.ContainerAddr) != 0 {
		t.Fatalf("static container_addr = %#v", static.ContainerAddr)
	}
}

func TestIchunqiuStartPassesQuestionID(t *testing.T) {
	var gotPath, gotQuery string
	client := challengeadapter.NewHTTPDriver(challengeadapter.HTTPDriverConfig{
		Manifest: mustIchunqiu(t),
		BaseURL:  "http://terminator.test",
		Token:    "team-secret",
		Client: &http.Client{Transport: roundTrip(func(request *http.Request) *http.Response {
			gotPath = request.URL.Path
			gotQuery = request.URL.RawQuery
			return jsonResponse(`{"code": 0, "message": "操作成功"}`)
		})},
		Timeout: time.Second,
	})
	if _, err := client.Start(context.Background(), "q-pwn-1"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/deed3dba39e57b7cf95ea63ddd84e0c8" {
		t.Fatalf("path = %q", gotPath)
	}
	for _, want := range []string{"token=team-secret", "question_id=q-pwn-1"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query = %q, want %q", gotQuery, want)
		}
	}
}

func TestIchunqiuStartRejectsNonZeroEnvelopeCode(t *testing.T) {
	client := challengeadapter.NewHTTPDriver(challengeadapter.HTTPDriverConfig{
		Manifest: mustIchunqiu(t),
		BaseURL:  "http://terminator.test",
		Token:    "team-secret",
		Client: &http.Client{Transport: roundTrip(func(*http.Request) *http.Response {
			return jsonResponse(`{"code": 1, "message": "该题目不支持重置"}`)
		})},
		Timeout: time.Second,
	})
	if _, err := client.Start(context.Background(), "q-misc-1"); err == nil {
		t.Fatal("expected envelope error")
	} else if !strings.Contains(err.Error(), "该题目不支持重置") {
		t.Fatalf("error = %v", err)
	}
}

func TestIchunqiuSubmitEncodesFlagAndReadsStatus(t *testing.T) {
	var gotQuery url.Values
	client := challengeadapter.NewHTTPDriver(challengeadapter.HTTPDriverConfig{
		Manifest: mustIchunqiu(t),
		BaseURL:  "http://terminator.test",
		Token:    "team-secret",
		Client: &http.Client{Transport: roundTrip(func(request *http.Request) *http.Response {
			gotQuery = request.URL.Query()
			return jsonResponse(`{"code": 0, "message": "答案正确", "status": 1}`)
		})},
		Timeout: time.Second,
	})
	result, err := client.Submit(context.Background(), "q-pwn-1", "flag{a b&c}")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("token") != "team-secret" || gotQuery.Get("question_id") != "q-pwn-1" || gotQuery.Get("answer") != "flag{a b&c}" {
		t.Fatalf("query = %#v", gotQuery)
	}
	if !result.Correct || result.Message != "答案正确" {
		t.Fatalf("result = %#v", result)
	}
}

func TestIchunqiuSubmitWrongFlagIsNotAnError(t *testing.T) {
	client := challengeadapter.NewHTTPDriver(challengeadapter.HTTPDriverConfig{
		Manifest: mustIchunqiu(t),
		BaseURL:  "http://terminator.test",
		Token:    "team-secret",
		Client: &http.Client{Transport: roundTrip(func(*http.Request) *http.Response {
			return jsonResponse(`{"code": 1, "message": "答案错误", "status": 0}`)
		})},
		Timeout: time.Second,
	})
	result, err := client.Submit(context.Background(), "q-pwn-1", "flag{wrong}")
	if err != nil {
		t.Fatal(err)
	}
	if result.Correct || result.Message != "答案错误" {
		t.Fatalf("result = %#v", result)
	}
}

func TestIchunqiuListRejectsNonZeroEnvelopeCode(t *testing.T) {
	client := challengeadapter.NewHTTPDriver(challengeadapter.HTTPDriverConfig{
		Manifest: mustIchunqiu(t),
		BaseURL:  "http://terminator.test",
		Token:    "team-secret",
		Client: &http.Client{Transport: roundTrip(func(*http.Request) *http.Response {
			return jsonResponse(`{"code": 401, "message": "token无效 team-secret"}`)
		})},
		Timeout: time.Second,
	})
	_, err := client.List(context.Background())
	if err == nil {
		t.Fatal("expected envelope error")
	}
	if strings.Contains(err.Error(), "team-secret") {
		t.Fatalf("token leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") || !strings.Contains(err.Error(), "token无效") {
		t.Fatalf("error = %v", err)
	}
}

func TestIchunqiuSubmitRequiresConfiguredStatusPath(t *testing.T) {
	client := challengeadapter.NewHTTPDriver(challengeadapter.HTTPDriverConfig{
		Manifest: mustIchunqiu(t),
		BaseURL:  "http://terminator.test",
		Token:    "team-secret",
		Client: &http.Client{Transport: roundTrip(func(*http.Request) *http.Response {
			return jsonResponse(`{"code": 0, "message": "答案正确"}`)
		})},
		Timeout: time.Second,
	})
	if _, err := client.Submit(context.Background(), "q-pwn-1", "flag{x}"); err == nil {
		t.Fatal("expected missing status error")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
