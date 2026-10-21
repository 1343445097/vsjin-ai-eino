package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vsjin/vsjin-ai/runtimewire"
)

const bodyLimitServiceToken = "body-limit-test-service"
const bodyLimitRunToken = "body-limit-test-run-token-at-least-24"

// Exercise the production Handler through Hertz's HTTP parser. PerformRequest
// and httptest.NewServer(runtimewire.New(...)) bypass its transport body limit.
func startBodyLimitServer(t *testing.T) (string, *http.Client) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	s, _, cleanup := newTestServer(t, nil)
	t.Cleanup(cleanup)
	s.cfg.Host, s.cfg.Port, s.cfg.APIToken = "127.0.0.1", port, bodyLimitServiceToken
	s.wire = runtimewire.New(bodyLimitServiceToken)
	h := s.Handler()
	done := make(chan error, 1)
	go func() { done <- h.Run() }()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.Shutdown(ctx); err != nil {
			t.Errorf("Hertz shutdown: %v", err)
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("Hertz did not stop")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("Hertz startup: %v", err)
		default:
		}
		response, err := client.Get("http://" + addr + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return "http://" + addr, client
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Hertz did not become healthy")
	return "", nil
}

func postBodyLimitRequest(t *testing.T, client *http.Client, url string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bodyLimitServiceToken)
	req.Header.Set("X-VSJin-Run-Token", bodyLimitRunToken)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestHertzRuntimeAcceptsLargeJPEGOverTCP(t *testing.T) {
	url, client := startBodyLimitServer(t)
	// Deterministic camera-like noise produces a real JPEG of approximately
	// 6.2 MB, without customer images, fixture files or a model connection.
	picture := image.NewRGBA(image.Rect(0, 0, 2640, 2640))
	_, _ = rand.New(rand.NewSource(1)).Read(picture.Pix)
	var jpegBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, picture, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	if jpegBytes.Len() < 6_000_000 || jpegBytes.Len() > 7_000_000 {
		t.Fatalf("unexpected JPEG fixture size: %d", jpegBytes.Len())
	}
	if _, err := jpeg.Decode(bytes.NewReader(jpegBytes.Bytes())); err != nil {
		t.Fatalf("fixture is not a valid JPEG: %v", err)
	}
	dataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes.Bytes())
	body, err := json.Marshal(runtimewire.Request{
		Version: "1", RunID: "large-jpeg", RunToken: bodyLimitRunToken,
		Messages: []runtimewire.Message{{Role: "user", ContentParts: []runtimewire.ContentPart{
			{Type: "text", Text: "Describe this synthetic test image."},
			{Type: "image_url", ImageURL: &runtimewire.ImageURLPart{URL: dataURL}},
		}}},
		MaxIterations: 2, TimeoutSeconds: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("JPEG bytes=%d; JSON request bytes=%d", jpegBytes.Len(), len(body))
	response := postBodyLimitRequest(t, client, url+"/v1/runs", body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("large JPEG start status=%d, want 200", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 24<<20)
	sawModel, completed := false, false
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event runtimewire.Event
		if err := json.Unmarshal(bytes.TrimPrefix(scanner.Bytes(), []byte("data: ")), &event); err != nil {
			t.Fatal(err)
		}
		switch event.Type {
		case "model.request":
			sawModel = true
			foundImage := false
			for _, message := range event.Messages {
				for _, part := range message.ContentParts {
					if part.ImageURL != nil && part.ImageURL.URL == dataURL {
						foundImage = true
					}
				}
			}
			if !foundImage {
				t.Fatal("model.request lost or changed the authorized JPEG")
			}
			// Raising the transport limit must not relax the response decoder's
			// separate 4 MiB budget. Then complete the real wire exchange locally.
			oversized, _ := json.Marshal(runtimewire.Response{RequestID: event.RequestID, Delta: strings.Repeat("x", 4<<20)})
			rejected := postBodyLimitRequest(t, client, url+"/v1/runs/large-jpeg/responses", oversized)
			io.Copy(io.Discard, rejected.Body)
			rejected.Body.Close()
			if rejected.StatusCode != http.StatusBadRequest {
				t.Fatalf("response over 4 MiB status=%d, want 400", rejected.StatusCode)
			}
			reply, _ := json.Marshal(runtimewire.Response{RequestID: event.RequestID, Message: &runtimewire.Message{Role: "assistant", Content: "Synthetic image received."}})
			accepted := postBodyLimitRequest(t, client, url+"/v1/runs/large-jpeg/responses", reply)
			accepted.Body.Close()
			if accepted.StatusCode != http.StatusNoContent {
				t.Fatalf("model callback status=%d, want 204", accepted.StatusCode)
			}
		case "run.completed":
			completed = event.Final == "Synthetic image received."
		case "run.failed":
			t.Fatalf("run failed: %s", event.Error)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawModel || !completed {
		t.Fatalf("model.request=%v, completed=%v", sawModel, completed)
	}
}

func TestHertzRuntimeRejectsBodyOver24MiBOverTCP(t *testing.T) {
	url, client := startBodyLimitServer(t)
	response := postBodyLimitRequest(t, client, url+"/v1/runs", bytes.Repeat([]byte(" "), (24<<20)+1))
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("body over 24 MiB status=%d, want 413", response.StatusCode)
	}
}
