package daemon

import "net/http"

func authorizeOperatorTestRequest(server *Server, request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+server.operatorToken)
}
