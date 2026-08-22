//go:build web && !darwin

package main

import _ "embed"

//go:embed localattachwebapp.zip
var webappZip []byte
