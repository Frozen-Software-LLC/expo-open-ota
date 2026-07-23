package cdn

import (
	"errors"
	"expo-open-ota/config"
	"fmt"
	"net/url"
	"strings"
)

type R2PublicCDN struct{}

func getR2PublicDomain() string {
	return config.GetEnv("R2_PUBLIC_CDN_DOMAIN")
}

func (c *R2PublicCDN) isCDNAvailable() bool {
	return config.GetEnv("STORAGE_MODE") == "s3" && getR2PublicDomain() != ""
}

func (c *R2PublicCDN) ComputeRedirectionURLForAsset(branch, runtimeVersion, updateId, asset string) (string, error) {
	domain := getR2PublicDomain()
	if domain == "" {
		return "", errors.New("R2 public CDN domain is not configured")
	}
	keyPrefix := config.GetEnv("S3_KEY_PREFIX")
	if keyPrefix != "" && !strings.HasSuffix(keyPrefix, "/") {
		keyPrefix += "/"
	}
	key := fmt.Sprintf("%s%s/%s/%s/%s", keyPrefix, branch, runtimeVersion, updateId, asset)
	u := url.URL{Scheme: "https", Host: domain, Path: "/" + key}
	return u.String(), nil
}
