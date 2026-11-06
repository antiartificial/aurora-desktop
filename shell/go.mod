module github.com/antiartificial/aurora/shell

go 1.27

replace github.com/antiartificial/aurora/bus => ../bus

require (
	github.com/antiartificial/aurora/bus v0.0.0
	github.com/antiartificial/aurora/controller v0.0.0
)

require golang.org/x/sys v0.49.0 // indirect

replace github.com/antiartificial/aurora/controller => ../controller
