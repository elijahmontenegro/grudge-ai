module github.com/emontenegr/spidey/service

go 1.25.0

require (
	github.com/99designs/gqlgen v0.17.89
	github.com/emontenegr/spidey/core v0.0.0-00010101000000-000000000000
	github.com/emontenegr/spidey/gen/go v0.0.0
	github.com/emontenegr/spidey/rrc v0.0.0-00010101000000-000000000000
	github.com/vektah/gqlparser/v2 v2.5.32
	github.com/zalando/go-keyring v0.2.8
	google.golang.org/protobuf v1.36.11
	modernc.org/sqlite v1.48.1
)

require (
	fyne.io/systray v1.12.0 // indirect
	github.com/agnivade/levenshtein v1.2.1 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/sosodev/duration v1.4.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	modernc.org/libc v1.70.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace (
	github.com/emontenegr/spidey/core => ../core
	github.com/emontenegr/spidey/gen/go => ../gen/go
	github.com/emontenegr/spidey/rrc => ../rrc
)
