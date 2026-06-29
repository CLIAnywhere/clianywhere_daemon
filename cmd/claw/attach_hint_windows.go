//go:build cli && windows

package main

// menuHint Windows: warn user that mouse selection causes suspend
const menuHint = "↑↓/jk select  Enter confirm  q quit\nNOTE: Mouse text-select pauses console; press ↑↓ to resume"
