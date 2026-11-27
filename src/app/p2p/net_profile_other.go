//go:build !windows

package p2p

var isPublicNetworkProfileFn = isPublicNetworkProfile

func isPublicNetworkProfile() (bool, string) {
	return false, ""
}
