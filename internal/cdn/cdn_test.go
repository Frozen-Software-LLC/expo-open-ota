package cdn

import (
	"os"
	testing2 "testing"
)

func TestGetCDNReturnsGCSDirectWhenGCSConfigured(t *testing2.T) {
	os.Setenv("STORAGE_MODE", "gcs")
	os.Setenv("GCS_BUCKET_NAME", "test-bucket")
	os.Setenv("GOOGLE_APPLICATION_CREDENTIALS_B64", "e3ZhbHVlOiAxfQ==")
	ResetCDNInstance()
	c := GetCDN()
	if c == nil {
		t.Fatalf("expected CDN instance, got nil")
	}
	if _, ok := c.(*GCSDirectCDN); !ok {
		t.Fatalf("expected *GCSDirectCDN, got %T", c)
	}
}

func TestGetCDNReturnsR2PublicWhenConfigured(t *testing2.T) {
	os.Unsetenv("GCS_BUCKET_NAME")
	os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS_B64")
	os.Setenv("STORAGE_MODE", "s3")
	os.Setenv("R2_PUBLIC_CDN_DOMAIN", "ota-assets.dealseek.com")
	ResetCDNInstance()
	c := GetCDN()
	if c == nil {
		t.Fatalf("expected CDN instance, got nil")
	}
	if _, ok := c.(*R2PublicCDN); !ok {
		t.Fatalf("expected *R2PublicCDN, got %T", c)
	}
	u, err := c.ComputeRedirectionURLForAsset("prod", "1.60", "abc-123", "_expo/static/js/ios/entry-457.hbc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "https://ota-assets.dealseek.com/prod/1.60/abc-123/_expo/static/js/ios/entry-457.hbc"
	if u != expected {
		t.Fatalf("expected %s, got %s", expected, u)
	}
	os.Unsetenv("R2_PUBLIC_CDN_DOMAIN")
	ResetCDNInstance()
}

func TestGetCDNReturnsNilWithoutR2Domain(t *testing2.T) {
	os.Unsetenv("R2_PUBLIC_CDN_DOMAIN")
	os.Unsetenv("GCS_BUCKET_NAME")
	os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS_B64")
	os.Setenv("STORAGE_MODE", "s3")
	ResetCDNInstance()
	if c := GetCDN(); c != nil {
		t.Fatalf("expected nil CDN, got %T", c)
	}
}
