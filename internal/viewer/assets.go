package viewer

import _ "embed"

// ReaderJS is the unchanged MediaMTX 1.21.0 WebRTC reader.
//
//go:embed assets/reader.js
var ReaderJS []byte

// ReaderLicense is the reader's upstream MIT license.
//
//go:embed assets/mediamtx-LICENSE.txt
var ReaderLicense []byte
