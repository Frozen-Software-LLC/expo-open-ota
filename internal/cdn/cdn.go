package cdn

import "sync"

type CDN interface {
	isCDNAvailable() bool
	ComputeRedirectionURLForAsset(branch, runtimeVersion, updateId, asset string) (string, error)
}

var (
	cdnInstance CDN
	once        sync.Once
)

func GetCDN() CDN {
	once.Do(func() {
		cloudfrontCDN := CloudfrontCDN{}
		isCloudfrontCDNavailable := (&cloudfrontCDN).isCDNAvailable()
		if isCloudfrontCDNavailable {
			cdnInstance = &cloudfrontCDN
		} else {
			r2CDN := R2PublicCDN{}
			if (&r2CDN).isCDNAvailable() {
				cdnInstance = &r2CDN
			} else {
				gcsCDN := GCSDirectCDN{}
				if (&gcsCDN).isCDNAvailable() {
					cdnInstance = &gcsCDN
				}
			}
		}
	})
	return cdnInstance
}

func ResetCDNInstance() {
	cdnInstance = nil
	once = sync.Once{}
}
