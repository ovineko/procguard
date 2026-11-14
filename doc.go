// Package procguard supervises child OS processes without assigning application
// meaning to their exits. Each Child owns one launch; each Supervisor owns an
// independent restart loop. See the platform guarantees in the documentation
// before relying on orphan prevention or resource limits.
package procguard
