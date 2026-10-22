// Package gobluetti monitors and controls BLUETTI power stations through the
// BLUETTI cloud.
//
// It is an unofficial Go port of the cloud part of the BLUETTI Home Assistant
// integration. Bluetooth is not supported: every call goes through the cloud
// and needs an OAuth2 token for the user's BLUETTI account.
//
// Get a token with [Auth.LoginLocal] on a machine with a browser, or with
// [Auth.AuthCodeURL] and [Auth.Exchange] behind your own redirect URL, and
// renew it with [Auth.Refresh]. The gobluetti-login command wraps the browser
// login and writes the token to a file.
package gobluetti
