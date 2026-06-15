package amap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"agent_v3/internal/config"

	agenttool "trpc.group/trpc-go/trpc-agent-go/tool"
)

type recordedRequest struct {
	Path  string
	Query url.Values
}

func TestAmapToolsCallExpectedEndpoints(t *testing.T) {
	ctx := context.Background()
	requests := make(chan recordedRequest, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- recordedRequest{Path: r.URL.Path, Query: r.URL.Query()}
		if strings.HasSuffix(r.URL.Path, "/staticmap") {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "1",
			"info":     "OK",
			"infocode": "10000",
			"path":     r.URL.Path,
		})
	}))
	defer server.Close()

	runtime := newAmapRuntimeWithHTTPClient(config.AmapConfig{
		BaseURL:        server.URL + "/v4",
		APIKey:         "test-key",
		Output:         "JSON",
		TimeoutSeconds: 1,
	}, server.Client())
	tools := toolMap(newAmapTools(runtime))

	cases := []struct {
		name      string
		args      any
		wantPath  string
		wantQuery map[string]string
	}{
		{
			name:     "amap_poi_keyword_search",
			args:     AmapPOIKeywordSearchInput{Keywords: "景点", City: "上海", CityLimit: true, Offset: 10, Page: 1},
			wantPath: "/v4/place/text",
			wantQuery: map[string]string{
				"keywords":  "景点",
				"city":      "上海",
				"citylimit": "true",
				"offset":    "10",
				"page":      "1",
			},
		},
		{
			name:     "amap_poi_around_search",
			args:     AmapPOIAroundSearchInput{Location: "121.4737,31.2304", Keywords: "咖啡", Radius: 1200},
			wantPath: "/v4/place/around",
			wantQuery: map[string]string{
				"location": "121.4737,31.2304",
				"keywords": "咖啡",
				"radius":   "1200",
			},
		},
		{
			name:     "amap_poi_detail",
			args:     AmapPOIDetailInput{ID: "B000A8UIN8", Extensions: "all"},
			wantPath: "/v4/place/detail",
			wantQuery: map[string]string{
				"id":         "B000A8UIN8",
				"extensions": "all",
			},
		},
		{
			name:     "amap_input_tips",
			args:     AmapInputTipsInput{Keywords: "外滩", City: "上海", CityLimit: true},
			wantPath: "/v4/assistant/inputtips",
			wantQuery: map[string]string{
				"keywords":  "外滩",
				"city":      "上海",
				"citylimit": "true",
			},
		},
		{
			name:     "amap_geocode",
			args:     AmapGeocodeInput{Address: "上海外滩", City: "上海"},
			wantPath: "/v4/geocode/geo",
			wantQuery: map[string]string{
				"address": "上海外滩",
				"city":    "上海",
			},
		},
		{
			name:     "amap_regeocode",
			args:     AmapRegeoInput{Location: "121.4737,31.2304", Extensions: "all", Radius: 800},
			wantPath: "/v4/geocode/regeo",
			wantQuery: map[string]string{
				"location":   "121.4737,31.2304",
				"extensions": "all",
				"radius":     "800",
			},
		},
		{
			name:     "amap_distance",
			args:     AmapDistanceInput{Origins: []string{"121.4737,31.2304", "121.4998,31.2397"}, Destination: "121.4903,31.2416", Type: 3},
			wantPath: "/v4/distance",
			wantQuery: map[string]string{
				"origins":     "121.4737,31.2304|121.4998,31.2397",
				"destination": "121.4903,31.2416",
				"type":        "3",
			},
		},
		{
			name:     "amap_route_walking",
			args:     AmapWalkingRouteInput{Origin: "121.4737,31.2304", Destination: "121.4903,31.2416"},
			wantPath: "/v4/direction/walking",
			wantQuery: map[string]string{
				"origin":      "121.4737,31.2304",
				"destination": "121.4903,31.2416",
			},
		},
		{
			name:     "amap_route_transit",
			args:     AmapTransitRouteInput{Origin: "121.4737,31.2304", Destination: "121.4903,31.2416", City: "上海", Strategy: 2},
			wantPath: "/v4/direction/transit/integrated",
			wantQuery: map[string]string{
				"origin":      "121.4737,31.2304",
				"destination": "121.4903,31.2416",
				"city":        "上海",
				"strategy":    "2",
			},
		},
		{
			name: "amap_route_driving",
			args: AmapDrivingRouteInput{
				Origin:          "121.4737,31.2304",
				Destination:     "121.4903,31.2416",
				Waypoints:       []string{"121.4800,31.2300", "121.4850,31.2350"},
				RoadAggregation: true,
			},
			wantPath: "/v4/direction/driving",
			wantQuery: map[string]string{
				"origin":          "121.4737,31.2304",
				"destination":     "121.4903,31.2416",
				"waypoints":       "121.4800,31.2300;121.4850,31.2350",
				"roadaggregation": "true",
			},
		},
		{
			name:     "amap_route_bicycling",
			args:     AmapBicyclingRouteInput{Origin: "121.4737,31.2304", Destination: "121.4903,31.2416"},
			wantPath: "/v4/direction/bicycling",
			wantQuery: map[string]string{
				"origin":      "121.4737,31.2304",
				"destination": "121.4903,31.2416",
			},
		},
		{
			name:     "amap_district_search",
			args:     AmapDistrictSearchInput{Keywords: "上海", Subdistrict: 1, Extensions: "base"},
			wantPath: "/v4/config/district",
			wantQuery: map[string]string{
				"keywords":    "上海",
				"subdistrict": "1",
				"extensions":  "base",
			},
		},
		{
			name:     "amap_ip_location",
			args:     AmapIPLocationInput{IP: "114.247.50.2"},
			wantPath: "/v4/ip",
			wantQuery: map[string]string{
				"ip": "114.247.50.2",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := callTool(t, ctx, tools[tc.name], tc.args)
			resp, ok := out.(AmapResponse)
			if !ok {
				t.Fatalf("result type = %T, want AmapResponse", out)
			}
			if !resp.OK {
				t.Fatalf("response not ok: %+v", resp)
			}
			got := nextRequest(t, requests)
			if got.Path != tc.wantPath {
				t.Fatalf("path = %q, want %q", got.Path, tc.wantPath)
			}
			assertCommonQuery(t, got.Query)
			for key, want := range tc.wantQuery {
				if got.Query.Get(key) != want {
					t.Fatalf("query[%s] = %q, want %q; full query=%s", key, got.Query.Get(key), want, got.Query.Encode())
				}
			}
		})
	}

	t.Run("amap_static_map", func(t *testing.T) {
		out := callTool(t, ctx, tools["amap_static_map"], AmapStaticMapInput{
			Location: "121.4737,31.2304",
			Zoom:     12,
			Size:     "750*500",
			Traffic:  true,
			Validate: true,
		})
		resp, ok := out.(AmapStaticMapResult)
		if !ok {
			t.Fatalf("result type = %T, want AmapStaticMapResult", out)
		}
		if !resp.OK || !resp.Validated || resp.ContentType != "image/png" {
			t.Fatalf("unexpected static map result: %+v", resp)
		}
		if strings.Contains(resp.URLRedacted, "test-key") {
			t.Fatalf("URLRedacted leaked API key: %s", resp.URLRedacted)
		}
		got := nextRequest(t, requests)
		if got.Path != "/v4/staticmap" {
			t.Fatalf("path = %q, want /v4/staticmap", got.Path)
		}
		if got.Query.Get("output") != "" {
			t.Fatalf("static map should not inject output, got %q", got.Query.Get("output"))
		}
		if got.Query.Get("traffic") != "1" {
			t.Fatalf("traffic = %q, want 1", got.Query.Get("traffic"))
		}
		if got.Query.Get("key") != "test-key" {
			t.Fatalf("key = %q, want test-key", got.Query.Get("key"))
		}
	})
}

func TestAmapToolSetDeclarations(t *testing.T) {
	set := NewAmapToolSet(config.AmapConfig{BaseURL: "https://example.com", APIKey: "test-key"})
	if set.Name() != "amap" {
		t.Fatalf("Name() = %q", set.Name())
	}
	got := set.Tools(context.Background())
	if len(got) != 14 {
		t.Fatalf("tool count = %d, want 14", len(got))
	}
	names := map[string]bool{}
	for _, tl := range got {
		decl := tl.Declaration()
		if decl == nil {
			t.Fatal("nil declaration")
		}
		if decl.Name == "" || decl.Description == "" || decl.InputSchema == nil {
			t.Fatalf("incomplete declaration: %+v", decl)
		}
		if names[decl.Name] {
			t.Fatalf("duplicate tool name %q", decl.Name)
		}
		names[decl.Name] = true
	}
	for _, name := range []string{
		"amap_poi_keyword_search",
		"amap_poi_around_search",
		"amap_poi_detail",
		"amap_input_tips",
		"amap_geocode",
		"amap_regeocode",
		"amap_distance",
		"amap_route_walking",
		"amap_route_transit",
		"amap_route_driving",
		"amap_route_bicycling",
		"amap_district_search",
		"amap_ip_location",
		"amap_static_map",
	} {
		if !names[name] {
			t.Fatalf("missing tool %q", name)
		}
	}
}

func TestAmapToolsRequireAPIKey(t *testing.T) {
	tools := toolMap(NewAmapTools(config.AmapConfig{
		BaseURL: "https://example.com",
	}))
	_, err := callToolWithError(context.Background(), tools["amap_geocode"], AmapGeocodeInput{Address: "上海外滩"})
	if err == nil {
		t.Fatal("expected missing key error")
	}
	if !strings.Contains(err.Error(), "amap.api_key") {
		t.Fatalf("error %q should mention amap.api_key", err.Error())
	}
}

func TestAmapToolsRequireConfiguredBaseURL(t *testing.T) {
	tools := toolMap(NewAmapTools(config.AmapConfig{APIKey: "test-key"}))
	_, err := callToolWithError(context.Background(), tools["amap_geocode"], AmapGeocodeInput{Address: "上海外滩"})
	if err == nil {
		t.Fatal("expected missing baseurl error")
	}
	if !strings.Contains(err.Error(), "amap.baseurl") {
		t.Fatalf("error %q should mention amap.baseurl", err.Error())
	}
}

func toolMap(tools []agenttool.Tool) map[string]agenttool.Tool {
	out := make(map[string]agenttool.Tool, len(tools))
	for _, tl := range tools {
		out[tl.Declaration().Name] = tl
	}
	return out
}

func callTool(t *testing.T, ctx context.Context, tl agenttool.Tool, args any) any {
	t.Helper()
	out, err := callToolWithError(ctx, tl, args)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	return out
}

func callToolWithError(ctx context.Context, tl agenttool.Tool, args any) (any, error) {
	callable, ok := tl.(agenttool.CallableTool)
	if !ok {
		return nil, fmt.Errorf("tool %T is not callable", tl)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return callable.Call(ctx, raw)
}

func nextRequest(t *testing.T, requests <-chan recordedRequest) recordedRequest {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for request")
		return recordedRequest{}
	}
}

func assertCommonQuery(t *testing.T, q url.Values) {
	t.Helper()
	if q.Get("key") != "test-key" {
		t.Fatalf("key = %q, want test-key", q.Get("key"))
	}
	if q.Get("output") != "JSON" {
		t.Fatalf("output = %q, want JSON", q.Get("output"))
	}
}
