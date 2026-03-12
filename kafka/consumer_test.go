package kafka

import (
	"testing"
)

func TestParseMessage(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected ArticleHotEvent
		wantErr  bool
	}{
		{
			name:  "JSON完整消息",
			input: `{"article_id":"a_001","article_tag":"游戏评测","content":"文章正文","cover_url":"http://xxx.jpg"}`,
			expected: ArticleHotEvent{
				ArticleID:  "a_001",
				ArticleTag: "游戏评测",
				Content:    "文章正文",
				CoverUrl:   "http://xxx.jpg",
			},
			wantErr: false,
		},
		{
			name:  "JSON只有article_id和article_tag",
			input: `{"article_id":"a_002","article_tag":"科技"}`,
			expected: ArticleHotEvent{
				ArticleID:  "a_002",
				ArticleTag: "科技",
			},
			wantErr: false,
		},
		{
			name:    "JSON格式错误",
			input:   `{invalid json}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseMessage([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Errorf("期望错误但没有返回错误")
				}
				return
			}
			if err != nil {
				t.Errorf("不期望错误但返回了: %v", err)
				return
			}
			if result.ArticleID != tt.expected.ArticleID {
				t.Errorf("ArticleID 不匹配: got %s, want %s", result.ArticleID, tt.expected.ArticleID)
			}
			if result.ArticleTag != tt.expected.ArticleTag {
				t.Errorf("ArticleTag 不匹配: got %s, want %s", result.ArticleTag, tt.expected.ArticleTag)
			}
			if result.Content != tt.expected.Content {
				t.Errorf("Content 不匹配: got %s, want %s", result.Content, tt.expected.Content)
			}
			if result.CoverUrl != tt.expected.CoverUrl {
				t.Errorf("CoverUrl 不匹配: got %s, want %s", result.CoverUrl, tt.expected.CoverUrl)
			}
		})
	}
}
}
