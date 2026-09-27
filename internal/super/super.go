// Package super holds UC2's built-in "supermaster" dictionary (master prefix 0).
// The data is part of the UltraCompressor II release by Nico de Vries (LGPL-3.0).
package super

import _ "embed"

//go:embed super.dat
var Data string
