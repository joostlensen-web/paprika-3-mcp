package paprika

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roundTripFunc allows using a function as an http.RoundTripper
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// recordedRequest is a request captured by the fake Paprika server
type recordedRequest struct {
	Method string
	Host   string
	Path   string
	Body   []byte
	Header http.Header
}

// fakePaprika starts a test server and returns an http.Client that sends every request to it,
// whatever host the code under test uses. Requests are recorded in order.
func fakePaprika(t *testing.T, handler http.HandlerFunc) (*http.Client, func() []recordedRequest) {
	var mu sync.Mutex
	var requests []recordedRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		r.Body = io.NopCloser(bytes.NewReader(body))

		mu.Lock()
		requests = append(requests, recordedRequest{Method: r.Method, Host: r.Header.Get("X-Original-Host"), Path: r.URL.Path, Body: body, Header: r.Header.Clone()})
		mu.Unlock()

		handler(w, r)
	}))
	t.Cleanup(server.Close)

	target, err := url.Parse(server.URL)
	require.NoError(t, err)

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req = req.Clone(req.Context())
			req.Header.Set("X-Original-Host", req.URL.Host)
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			return http.DefaultTransport.RoundTrip(req)
		}),
	}

	return client, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), requests...)
	}
}

func newFakeClient(httpClient *http.Client) *Client {
	return &Client{client: httpClient, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// decodeUpload extracts and gunzips the "data" file from a multipart sync upload
func decodeUpload(t *testing.T, r recordedRequest) []byte {
	req, err := http.NewRequest(r.Method, "http://example.com", bytes.NewReader(r.Body))
	require.NoError(t, err)
	req.Header = r.Header

	file, _, err := req.FormFile("data")
	require.NoError(t, err)
	gz, err := gzip.NewReader(file)
	require.NoError(t, err)
	data, err := io.ReadAll(gz)
	require.NoError(t, err)
	return data
}

func okHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(`{"result": true}`))
}

func TestLogin(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
	}{
		{name: "simple credentials", username: "user@example.com", password: "password123"},
		{name: "special characters", username: "user@example.com", password: `a&b+c%d#e=f\g"hé`},
		{name: "percent-encoded lookalike", username: "user@example.com", password: "%26%3D"},
		{name: "plus addressing", username: "user+tag@example.com", password: "p@ss word"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, requests := fakePaprika(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
				w.Write([]byte(`{"result": {"token": "test-token"}}`))
			})

			token, err := login(context.Background(), *client, tt.username, tt.password)
			require.NoError(t, err)
			assert.Equal(t, "test-token", token)

			reqs := requests()
			require.Len(t, reqs, 1)
			assert.Equal(t, "/api/v1/account/login", reqs[0].Path)
			values, err := url.ParseQuery(string(reqs[0].Body))
			require.NoError(t, err)
			assert.Equal(t, tt.username, values.Get("email"))
			assert.Equal(t, tt.password, values.Get("password"))
		})
	}
}

func TestLoginError(t *testing.T) {
	client, _ := fakePaprika(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	_, err := login(context.Background(), *client, "user@example.com", "wrong")
	assert.ErrorContains(t, err, "failed to login")
}

func TestSaveRecipe(t *testing.T) {
	t.Run("nil categories are sent as an empty array", func(t *testing.T) {
		httpClient, requests := fakePaprika(t, okHandler)
		client := newFakeClient(httpClient)

		saved, err := client.SaveRecipe(context.Background(), Recipe{Name: "Soup"})
		require.NoError(t, err)
		assert.NotNil(t, saved.Categories)

		reqs := requests()
		require.Len(t, reqs, 2)
		assert.Equal(t, "/api/v2/sync/recipe/"+saved.UID+"/", reqs[0].Path)
		assert.Equal(t, "/api/v2/sync/notify", reqs[1].Path)

		var fields map[string]interface{}
		require.NoError(t, json.Unmarshal(decodeUpload(t, reqs[0]), &fields))
		assert.Equal(t, []interface{}{}, fields["categories"])
		assert.Equal(t, "Soup", fields["name"])
	})

	t.Run("existing created timestamp is kept", func(t *testing.T) {
		httpClient, _ := fakePaprika(t, okHandler)
		client := newFakeClient(httpClient)

		saved, err := client.SaveRecipe(context.Background(), Recipe{UID: "abc", Name: "Soup", Created: "2020-01-02 03:04:05"})
		require.NoError(t, err)
		assert.Equal(t, "2020-01-02 03:04:05", saved.Created)
		assert.Equal(t, "ABC", saved.UID)
	})

	t.Run("error in response body is returned", func(t *testing.T) {
		httpClient, _ := fakePaprika(t, func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"error": {"code": 1, "message": "nope"}}`))
		})
		client := newFakeClient(httpClient)

		_, err := client.SaveRecipe(context.Background(), Recipe{Name: "Soup"})
		assert.ErrorContains(t, err, "nope")
	})
}

func TestListMeals(t *testing.T) {
	httpClient, _ := fakePaprika(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/sync/meals/", r.URL.Path)
		w.Write([]byte(`{"result": [
			{"uid": "M1", "recipe_uid": null, "date": "2025-01-01 00:00:00", "type": 2, "name": "Pizza", "order_flag": 0, "type_uid": "T2", "scale": null, "is_ingredient": false},
			{"uid": "M2", "recipe_uid": "R1", "date": "2025-01-02 00:00:00", "type": 0, "name": "Porridge", "order_flag": 1, "type_uid": "T0", "scale": null, "is_ingredient": false}
		]}`))
	})
	client := newFakeClient(httpClient)

	meals, err := client.ListMeals(context.Background())
	require.NoError(t, err)
	require.Len(t, meals.Result, 2)
	assert.Nil(t, meals.Result[0].RecipeUID)
	assert.Equal(t, "Pizza", meals.Result[0].Name)
	require.NotNil(t, meals.Result[1].RecipeUID)
	assert.Equal(t, "R1", *meals.Result[1].RecipeUID)
	assert.Equal(t, "T0", meals.Result[1].TypeUID)
}

func TestListCategoriesAndMealTypes(t *testing.T) {
	httpClient, _ := fakePaprika(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/sync/categories/":
			w.Write([]byte(`{"result": [{"uid": "C1", "name": "Pasta", "order_flag": 0, "parent_uid": "C0", "cookbook_uid": null}, {"uid": "C0", "name": "Soort gerecht", "order_flag": 0, "parent_uid": null}]}`))
		case "/api/v2/sync/mealtypes/":
			w.Write([]byte(`{"result": [{"uid": "T1", "name": "Nagerecht", "order_flag": 4, "color": "#FFCC00", "export_all_day": false, "export_time": 72000, "original_type": 0}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	client := newFakeClient(httpClient)

	categories, err := client.ListCategories(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []Category{{UID: "C1", Name: "Pasta", ParentUID: "C0"}, {UID: "C0", Name: "Soort gerecht"}}, categories.Result)

	mealTypes, err := client.ListMealTypes(context.Background())
	require.NoError(t, err)
	require.Len(t, mealTypes.Result, 1)
	assert.Equal(t, "Nagerecht", mealTypes.Result[0].Name)
	assert.Equal(t, 0, mealTypes.Result[0].OriginalType)
}

func TestSaveMeal(t *testing.T) {
	httpClient, requests := fakePaprika(t, okHandler)
	client := newFakeClient(httpClient)

	recipeUID := "R1"
	saved, err := client.SaveMeal(context.Background(), Meal{RecipeUID: &recipeUID, Date: "2025-01-01 00:00:00", Type: 2, TypeUID: "T2", Name: "Pizza"})
	require.NoError(t, err)
	assert.NotEmpty(t, saved.UID)

	reqs := requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "www.paprikaapp.com", reqs[0].Host)
	assert.Equal(t, "/api/v2/sync/meals/", reqs[0].Path)
	assert.Equal(t, "/api/v2/sync/notify", reqs[1].Path)

	var sent []map[string]interface{}
	require.NoError(t, json.Unmarshal(decodeUpload(t, reqs[0]), &sent))
	require.Len(t, sent, 1)
	assert.Equal(t, saved.UID, sent[0]["uid"])
	assert.Equal(t, "R1", sent[0]["recipe_uid"])
	assert.Equal(t, "T2", sent[0]["type_uid"])
	assert.Equal(t, float64(2), sent[0]["type"])
	assert.Contains(t, sent[0], "scale")
}

func TestRetryTransport(t *testing.T) {
	t.Run("retries network errors and replays the body", func(t *testing.T) {
		var bodies []string
		transport := &retryTransport{maxRetries: 3, baseDelay: time.Millisecond, transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			bodies = append(bodies, string(b))
			if len(bodies) < 3 {
				return nil, errors.New("connection reset by peer")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(nil))}, nil
		})}

		req, err := http.NewRequest(http.MethodPost, "https://paprikaapp.com/x", bytes.NewBufferString("payload"))
		require.NoError(t, err)
		resp, err := transport.RoundTrip(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, []string{"payload", "payload", "payload"}, bodies)
	})

	t.Run("does not retry client errors", func(t *testing.T) {
		calls := 0
		transport := &retryTransport{maxRetries: 3, baseDelay: time.Millisecond, transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(bytes.NewReader(nil))}, nil
		})}

		req, err := http.NewRequest(http.MethodGet, "https://paprikaapp.com/x", nil)
		require.NoError(t, err)
		resp, err := transport.RoundTrip(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.Equal(t, 1, calls)
	})

	t.Run("gives up after max retries", func(t *testing.T) {
		calls := 0
		transport := &retryTransport{maxRetries: 2, baseDelay: time.Millisecond, transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("connection reset by peer")
		})}

		req, err := http.NewRequest(http.MethodGet, "https://paprikaapp.com/x", nil)
		require.NoError(t, err)
		_, err = transport.RoundTrip(req)
		assert.Error(t, err)
		assert.Equal(t, 3, calls)
	})
}

func TestToMarkdownNumbersDirectionSteps(t *testing.T) {
	r := Recipe{Name: "Soup", Directions: "Chop.\n\nBoil.\n \nServe."}
	assert.Contains(t, r.ToMarkdown(), "## Directions\n1. Chop.\n2. Boil.\n3. Serve.\n")
}
