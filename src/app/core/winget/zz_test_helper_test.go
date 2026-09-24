package winget

import "errors"

func errFromString(s string) error { return errors.New(s) }
