package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
)

func main() {
	address := flag.String("addr", "127.0.0.1:28641", "fixture listen address")
	flag.Parse()

	var submissions atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /", func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "/catalog", http.StatusFound)
	})
	mux.HandleFunc("GET /catalog", func(writer http.ResponseWriter, _ *http.Request) {
		writeHTML(writer, `<!doctype html>
<html><head><title>Athena packaged catalog</title></head><body>
<header><h1>Athena Video Catalog</h1></header>
<main aria-label="Video results">
  <label for="search">Search tutorials</label>
  <input id="search" role="searchbox" aria-label="Search tutorials" value="agent systems">
  <button aria-label="Search catalog">Search</button>
  <ol aria-label="Tutorial videos">
    <li><a aria-label="First video" href="/video/first">First tutorial video</a></li>
    <li><a aria-label="Second video" href="/video/second">Second tutorial video</a></li>
    <li><a aria-label="Third video" href="/video/third">Third tutorial video</a></li>
  </ol>
</main></body></html>`)
	})
	mux.HandleFunc("GET /video/", func(writer http.ResponseWriter, request *http.Request) {
		name := strings.Trim(strings.TrimPrefix(request.URL.Path, "/video/"), "/")
		if name == "" {
			http.NotFound(writer, request)
			return
		}
		title := name + " tutorial"
		writeHTML(writer, fmt.Sprintf(`<!doctype html>
<html><head><title>%[1]s</title></head><body><main>
<h1>%[1]s</h1>
<video id="player" aria-label="%[1]s video" controls muted width="480" height="270"></video>
<canvas id="frames" width="480" height="270" hidden></canvas>
<script>
const canvas = document.querySelector('#frames');
const context = canvas.getContext('2d');
let frame = 0;
setInterval(() => {
  context.fillStyle = frame %% 2 ? '#123047' : '#0c8f68';
  context.fillRect(0, 0, 480, 270);
  context.fillStyle = 'white';
  context.font = '32px sans-serif';
  context.fillText('Athena frame ' + frame++, 80, 140);
}, 80);
document.querySelector('#player').srcObject = canvas.captureStream(12);
</script></main></body></html>`, title))
	})
	mux.HandleFunc("GET /reference", func(writer http.ResponseWriter, _ *http.Request) {
		writeHTML(writer, `<!doctype html><html><head><title>Athena reference</title></head><body>
<main><h1>Reference page opened</h1><a aria-label="Continue reference" href="/reference/continued">Continue reference</a></main>
</body></html>`)
	})
	mux.HandleFunc("GET /reference/continued", func(writer http.ResponseWriter, _ *http.Request) {
		writeHTML(writer, `<!doctype html><html><head><title>Athena continued reference</title></head><body>
<main><h1>Stable tab continuation verified</h1></main></body></html>`)
	})
	mux.HandleFunc("GET /submission", func(writer http.ResponseWriter, _ *http.Request) {
		writeHTML(writer, `<!doctype html><html><head><title>Athena idempotency fixture</title></head><body>
<main><h1>Idempotency fixture</h1>
<button id="submit" aria-label="Submit exactly once">Submit exactly once</button>
<output id="result" aria-live="polite">Not submitted</output>
<script>
document.querySelector('#submit').addEventListener('click', async () => {
  const response = await fetch('/api/submissions', {method: 'POST'});
  const result = await response.json();
  document.querySelector('#result').textContent = 'Submissions: ' + result.count;
});
</script></main></body></html>`)
	})
	mux.HandleFunc("POST /api/submissions", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, map[string]any{"count": submissions.Add(1)})
	})
	mux.HandleFunc("GET /api/submissions", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, map[string]any{"count": submissions.Load()})
	})
	mux.HandleFunc("POST /api/submissions/reset", func(writer http.ResponseWriter, _ *http.Request) {
		submissions.Store(0)
		writeJSON(writer, map[string]any{"count": 0})
	})

	log.Printf("Athena v0.3 browser fixture listening on http://%s", *address)
	if err := http.ListenAndServe(*address, mux); err != nil {
		log.Fatal(err)
	}
}

func writeHTML(writer http.ResponseWriter, value string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write([]byte(value))
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}
