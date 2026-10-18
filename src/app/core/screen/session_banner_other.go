//go:build !windows

package screen

import "fmt"

type BannerOptions struct {
	Operator        string
	Text            string
	AllowDisconnect bool
	OnDisconnect    func()
}

type SessionBanner struct{}

func NewSessionBanner(BannerOptions) (*SessionBanner, error) {
	return nil, fmt.Errorf("banner de sessão disponível apenas no Windows")
}

func (b *SessionBanner) Close() {}
