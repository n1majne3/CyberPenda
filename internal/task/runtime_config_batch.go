package task

import (
	"encoding/json"
	"fmt"
	"strings"
)

func (s *Service) LatestRuntimeConfigs(taskIDs []string) (map[string]map[string]any, error) {
	configs := make(map[string]map[string]any)
	// Bound placeholders for large Task lists and avoid repeated owner lookups.
	ids := make([]string, 0, len(taskIDs))
	seen := make(map[string]bool, len(taskIDs))
	for _, id := range taskIDs {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for offset := 0; offset < len(ids); offset += 500 {
		end := min(offset+500, len(ids))
		args := make([]any, 0, end-offset)
		for _, id := range ids[offset:end] {
			args = append(args, id)
		}
		rows, err := s.db.Query(`SELECT task_id,config_json FROM task_runtime_config_versions v
   WHERE task_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)
   AND version=(SELECT MAX(version) FROM task_runtime_config_versions WHERE task_id=v.task_id)`, args...)
		if err != nil {
			return nil, fmt.Errorf("read latest Task Runtime configurations: %w", err)
		}
		for rows.Next() {
			var id, raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return nil, err
			}
			var config map[string]any
			// Activity projection treats an unreadable snapshot as unavailable, as the
			// single-Task resolver does. It must not fail every other Task in the list.
			if json.Unmarshal([]byte(raw), &config) == nil {
				configs[id] = config
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return configs, nil
}
