package native

import (
"context"
"fmt"
"testing"
)

func TestCollectNetworksIPsManual(t *testing.T) {
items, _ := collectNetworksNative(context.Background())
for _, n := range items {
fmt.Printf("name=%q ipv4=%q gw=%q\n", n.FriendlyName, n.IPv4, n.Gateway)
}
}
