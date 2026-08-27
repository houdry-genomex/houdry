package openaicompat

import "time"

func unixNow() int64 { return time.Now().Unix() }
