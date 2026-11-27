package linkserver

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Against fake-gcs-server (STORAGE_EMULATOR_HOST) or a real bucket when
// PRISM_TEST_GCS names one (gs://bucket): the Blob contract, and two stores
// sharing the bucket.
func TestGCSBlobAgainstARealServer(t *testing.T) {
	u := os.Getenv("PRISM_TEST_GCS")
	if u == "" {
		t.Skip("PRISM_TEST_GCS not set")
	}
	ctx := context.Background()
	if emu := os.Getenv("STORAGE_EMULATOR_HOST"); emu != "" {
		bucket := strings.TrimPrefix(u, "gs://")
		bucket, _, _ = strings.Cut(bucket, "/")
		r, err := http.Post("http://"+strings.TrimPrefix(emu, "http://")+"/storage/v1/b", "application/json", strings.NewReader(`{"name":"`+bucket+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
	}
	prefix := strings.TrimRight(u, "/") + "/" + t.Name() + "-" + time.Now().Format("150405.000000")
	b, err := OpenGCS(ctx, GCSConfig{URL: prefix})
	if err != nil {
		t.Fatal(err)
	}
	blobContract(t, b)
	s1, err := OpenStore(ctx, b, Options{Keep: 5})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(ctx, b, Options{Keep: 5})
	if err != nil {
		t.Fatal(err)
	}
	replicaContract(t, s1, s2)
}
