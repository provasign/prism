package linkserver

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// The GET Object example from AWS's Signature Version 4 documentation
// ("Example: GET Object", examplebucket/test.txt with a Range header).
func TestSignV4MatchesTheAWSExample(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	c := credential{id: "AKIAIOSFODNN7EXAMPLE", secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	signV4(req, nil, c, "us-east-1", time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization:\n got %s\nwant %s", got, want)
	}
}

// Against a real S3-compatible server when PRISM_TEST_S3 names one
// (s3://bucket/prefix; endpoint in PRISM_TEST_S3_ENDPOINT, path style):
// the Blob contract, and two stores sharing the bucket.
func TestS3BlobAgainstARealServer(t *testing.T) {
	u := os.Getenv("PRISM_TEST_S3")
	if u == "" {
		t.Skip("PRISM_TEST_S3 not set")
	}
	ctx := context.Background()
	prefix := strings.TrimRight(u, "/") + "/" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + time.Now().Format("150405.000000")
	b, err := OpenS3(ctx, S3Config{URL: prefix, Endpoint: os.Getenv("PRISM_TEST_S3_ENDPOINT"), PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	blobContract(t, b)

	// Two replicas on one bucket: each sees the other's upload after a
	// refresh, and concurrent uploads to one repository lose no branch.
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
