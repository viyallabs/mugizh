package app

import (
	"encoding/base64"
	"strings"
	"testing"
)

// infraEnv is a complete, synthetic environment for LoadSettings; tests override single keys.
func infraEnv(over map[string]string) func(string) string {
	b := base64.StdEncoding.EncodeToString(make([]byte, 32))
	m := map[string]string{
		"APP_ENV": "staging", "MUGIZH_BLIND_INDEX_KEY": b, "MUGIZH_SCHEDULER_CALLBACK_SECRET": b, "TRUSTED_PROXIES": "1",
		"MUGIZH_KEY_PROVIDER": "aws-kms", "MUGIZH_KMS_KEY_ID": "arn:aws:kms:xx-region-1:000000000000:key/mrk-synthetic",
		"AWS_REGION": "xx-region-1", "MUGIZH_ALLOWED_REGIONS": "xx-region-1, xx-region-2",
		"MUGIZH_BLOB_STORE": "s3", "MUGIZH_BLOB_BUCKET": "synthetic-blobs", "MUGIZH_WORM_BUCKET": "synthetic-worm",
	}
	for k, v := range over {
		m[k] = v
	}
	return func(k string) string { return m[k] }
}

func TestStagingWithKMSAndBucketsPassesSettings(t *testing.T) {
	s, err := LoadSettings(infraEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.KeyProvider != "aws-kms" || s.BlobStore != "s3" || len(s.AllowedRegions) != 2 || s.AllowedRegions[1] != "xx-region-2" {
		t.Fatalf("%+v", s)
	}
	if err := s.refuseUnbuilt(); err != nil {
		t.Fatalf("staging may run on the vendor fakes: %v", err)
	}
}

func TestStagingRefusesLocalOrIncompleteInfra(t *testing.T) {
	cases := map[string]map[string]string{
		"local key provider":  {"MUGIZH_KEY_PROVIDER": ""},
		"local blob store":    {"MUGIZH_BLOB_STORE": ""},
		"no key id":           {"MUGIZH_KMS_KEY_ID": ""},
		"no blob bucket":      {"MUGIZH_BLOB_BUCKET": ""},
		"no worm bucket":      {"MUGIZH_WORM_BUCKET": ""},
		"same bucket":         {"MUGIZH_WORM_BUCKET": "synthetic-blobs"},
		"no region":           {"AWS_REGION": ""},
		"no allowed regions":  {"MUGIZH_ALLOWED_REGIONS": ""},
		"region not allowed":  {"AWS_REGION": "xx-region-9"},
		"custom endpoint":     {"MUGIZH_S3_ENDPOINT": "http://localhost:9000"},
		"unknown provider":    {"MUGIZH_KEY_PROVIDER": "gcp-kms"},
		"unknown blob store":  {"MUGIZH_BLOB_STORE": "gcs"},
		"production, no kms":  {"APP_ENV": "production", "MUGIZH_KEY_PROVIDER": "local"},
		"production, bad reg": {"APP_ENV": "production", "AWS_REGION": "xx-region-9"},
	}
	for name, over := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadSettings(infraEnv(over)); err == nil {
				t.Fatal("settings accepted")
			}
		})
	}
}

func TestProductionRefusesUntilVendorAdaptersExist(t *testing.T) {
	s, err := LoadSettings(infraEnv(map[string]string{"APP_ENV": "production"}))
	if err != nil {
		t.Fatalf("production settings stage: %v", err)
	}
	err = s.refuseUnbuilt()
	if err == nil || !strings.Contains(err.Error(), "SMS gateway, payment aggregator and video adapters") {
		t.Fatalf("got %v", err)
	}
}

func TestDevLikeKeepsLocalDefaults(t *testing.T) {
	for _, env := range []string{"local", "test", "dev"} {
		s, err := LoadSettings(infraEnv(map[string]string{
			"APP_ENV": env, "MUGIZH_KEY_PROVIDER": "", "MUGIZH_BLOB_STORE": "", "MUGIZH_KMS_KEY_ID": "", "AWS_REGION": "",
			"MUGIZH_ALLOWED_REGIONS": "", "MUGIZH_BLOB_BUCKET": "", "MUGIZH_WORM_BUCKET": "", "TRUSTED_PROXIES": "",
		}))
		if err != nil {
			t.Fatalf("%s: %v", env, err)
		}
		if s.KeyProvider != "local" || s.BlobStore != "local" || !s.devLike() {
			t.Fatalf("%s: %+v", env, s)
		}
		if err := s.refuseUnbuilt(); err != nil {
			t.Fatalf("%s: %v", env, err)
		}
	}
	// Dev may use an S3-compatible endpoint (OCI, MinIO).
	if _, err := LoadSettings(infraEnv(map[string]string{"APP_ENV": "dev", "MUGIZH_S3_ENDPOINT": "http://localhost:9000", "MUGIZH_S3_PATH_STYLE": "true", "MUGIZH_ALLOWED_REGIONS": ""})); err != nil {
		t.Fatalf("dev with endpoint: %v", err)
	}
}
