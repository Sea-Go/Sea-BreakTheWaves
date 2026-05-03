package kafka

import "testing"

func TestParseMessage(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected ArticleSyncEvent
		wantErr  bool
	}{
		{
			name:  "full payload",
			input: `{"event_scope":"article_sync","event_id":"evt-1","article_id":"a_001","op":"upsert","reason":"create","author_id":"42","status":"PUBLISHED","version_ms":123,"title":"demo","brief":"brief","cover_url":"http://xxx.jpg","manual_type_tag":"game-review","secondary_tags":["tech","demo"],"markdown":"# demo"}`,
			expected: ArticleSyncEvent{
				EventScope:    "article_sync",
				EventID:       "evt-1",
				ArticleID:     "a_001",
				Op:            "upsert",
				Reason:        "create",
				AuthorID:      "42",
				Status:        "PUBLISHED",
				VersionMs:     123,
				Title:         "demo",
				Brief:         "brief",
				CoverURL:      "http://xxx.jpg",
				ManualTypeTag: "game-review",
				SecondaryTags: []string{"tech", "demo"},
				Markdown:      "# demo",
			},
		},
		{
			name:  "minimal valid payload",
			input: `{"event_id":"evt-2","article_id":"a_002","op":"delete"}`,
			expected: ArticleSyncEvent{
				EventScope: ArticleSyncScope,
				EventID:    "evt-2",
				ArticleID:  "a_002",
				Op:         "delete",
			},
		},
		{
			name:    "invalid json",
			input:   `{invalid json}`,
			wantErr: true,
		},
		{
			name:    "missing article id",
			input:   `{"event_id":"evt-3","op":"upsert"}`,
			wantErr: true,
		},
		{
			name:    "missing op",
			input:   `{"event_id":"evt-4","article_id":"a_004"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseMessage([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.EventScope != tt.expected.EventScope ||
				result.EventID != tt.expected.EventID ||
				result.ArticleID != tt.expected.ArticleID ||
				result.Op != tt.expected.Op ||
				result.Reason != tt.expected.Reason ||
				result.AuthorID != tt.expected.AuthorID ||
				result.Status != tt.expected.Status ||
				result.VersionMs != tt.expected.VersionMs ||
				result.Title != tt.expected.Title ||
				result.Brief != tt.expected.Brief ||
				result.CoverURL != tt.expected.CoverURL ||
				result.ManualTypeTag != tt.expected.ManualTypeTag ||
				result.Markdown != tt.expected.Markdown {
				t.Fatalf("unexpected parse result: %#v", result)
			}
			if len(result.SecondaryTags) != len(tt.expected.SecondaryTags) {
				t.Fatalf("unexpected secondary_tags: %#v", result.SecondaryTags)
			}
			for i := range result.SecondaryTags {
				if result.SecondaryTags[i] != tt.expected.SecondaryTags[i] {
					t.Fatalf("unexpected secondary_tags: %#v", result.SecondaryTags)
				}
			}
		})
	}
}
