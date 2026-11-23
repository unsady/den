package backup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/unsady/den/internal/config"
)

// BucketSecret holds the S3 bucket snapshots also go to, as Bucket's JSON.
const BucketSecret = "backup_s3"

// Bucket is an S3-compatible bucket, such as one of AWS, Cloudflare R2, or
// Backblaze B2, which den reaches by path, as endpoint/bucket/key.
type Bucket struct {
	// Endpoint is the service's address, such as
	// https://s3.us-east-1.amazonaws.com.
	Endpoint string `json:"endpoint"`
	// Region is the bucket's region, such as us-east-1, or auto for R2.
	Region string `json:"region"`
	Name   string `json:"bucket"`
	// Prefix comes before each snapshot's name, such as den/.
	Prefix string `json:"prefix,omitempty"`
	KeyID  string `json:"key_id"`
	Secret string `json:"secret"`

	client *http.Client
	now    func() time.Time
}

var (
	bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	keyPrefix  = regexp.MustCompile(`^[A-Za-z0-9._/-]*$`)
)

// Check reports what is wrong with b, if anything.
func (b Bucket) Check() error {
	u, err := url.Parse(b.Endpoint)
	switch {
	case err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/"):
		return errors.New("the endpoint is an https address with no path, such as https://s3.us-east-1.amazonaws.com")
	case b.Region == "":
		return errors.New("the region is missing")
	case !bucketName.MatchString(b.Name):
		return fmt.Errorf("%q is not a bucket name", b.Name)
	case !keyPrefix.MatchString(b.Prefix) || strings.HasPrefix(b.Prefix, "/"):
		return errors.New("the prefix takes letters, digits, and . _ - /, and does not start with /")
	case b.KeyID == "" || b.Secret == "":
		return errors.New("the access key is missing")
	}
	return nil
}

// loadBucket reads the bucket from den's secrets, or returns nil when
// there is none.
func loadBucket(cfg config.Config) (*Bucket, error) {
	data, err := os.ReadFile(filepath.Join(cfg.Secrets, BucketSecret))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var b Bucket
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("the secret %s: %w", BucketSecret, err)
	}
	if err := b.Check(); err != nil {
		return nil, fmt.Errorf("the secret %s: %w", BucketSecret, err)
	}
	return &b, nil
}

// save uploads the snapshot and deletes the ones beyond the latest Keep.
func (b *Bucket) save(ctx context.Context, name string, data []byte) error {
	if _, err := b.do(ctx, http.MethodPut, b.Prefix+name, nil, data); err != nil {
		return err
	}

	names, err := b.snapshots(ctx)
	if err != nil {
		return err
	}
	for _, old := range stale(names, name) {
		if _, err := b.do(ctx, http.MethodDelete, b.Prefix+old, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// snapshots lists the snapshots' names under the prefix, oldest first.
func (b *Bucket) snapshots(ctx context.Context) ([]string, error) {
	var names []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {b.Prefix + prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		body, err := b.do(ctx, http.MethodGet, "", q, nil)
		if err != nil {
			return nil, err
		}

		var page struct {
			Contents []struct {
				Key string
			}
			IsTruncated           bool
			NextContinuationToken string
		}
		if err := xml.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("list: %w", err)
		}
		for _, c := range page.Contents {
			name := strings.TrimPrefix(c.Key, b.Prefix)
			if isSnapshot(name) && !strings.Contains(name, "/") {
				names = append(names, name)
			}
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			break
		}
		token = page.NextContinuationToken
	}
	slices.Sort(names)
	return names, nil
}

// do sends a signed request for key in the bucket, or for the bucket
// itself when key is "", and returns the response's body.
func (b *Bucket) do(ctx context.Context, method, key string, query url.Values, body []byte) ([]byte, error) {
	path := "/" + b.Name
	if key != "" {
		path += "/" + key
	}
	u := strings.TrimSuffix(b.Endpoint, "/") + path
	if len(query) > 0 {
		u += "?" + canonicalQuery(query)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	now := time.Now
	if b.now != nil {
		now = b.now
	}
	b.sign(req, body, now())

	client := b.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		var e struct{ Code, Message string }
		if xml.Unmarshal(data, &e) == nil && e.Code != "" {
			return nil, fmt.Errorf("%s %s: %s: %s", method, path, e.Code, e.Message)
		}
		return nil, fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	return data, nil
}

// sign adds AWS Signature Version 4 to req, over its host, the headers it
// has, and body.
func (b *Bucket) sign(req *http.Request, body []byte, now time.Time) {
	sum := sha256.Sum256(body)
	payload := hex.EncodeToString(sum[:])
	stamp := now.UTC().Format("20060102T150405Z")
	day := stamp[:8]
	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", payload)

	headers := map[string]string{"host": req.URL.Host}
	for name, values := range req.Header {
		headers[strings.ToLower(name)] = strings.TrimSpace(strings.Join(values, ","))
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	var canonical strings.Builder
	for _, name := range names {
		canonical.WriteString(name + ":" + headers[name] + "\n")
	}
	signed := strings.Join(names, ";")
	request := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		canonical.String(),
		signed,
		payload,
	}, "\n")

	scope := day + "/" + b.Region + "/s3/aws4_request"
	hash := sha256.Sum256([]byte(request))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(hash[:])

	key := mac([]byte("AWS4"+b.Secret), day)
	key = mac(key, b.Region)
	key = mac(key, "s3")
	key = mac(key, "aws4_request")
	signature := hex.EncodeToString(mac(key, toSign))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+b.KeyID+"/"+scope+
		",SignedHeaders="+signed+",Signature="+signature)
}

func mac(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// canonicalQuery encodes q as SigV4 wants it: sorted, with a space as %20.
func canonicalQuery(q url.Values) string {
	return strings.ReplaceAll(q.Encode(), "+", "%20")
}
