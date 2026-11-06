module github.com/antiartificial/aurora/gateway

go 1.27

replace github.com/antiartificial/aurora/bus => ../bus

replace github.com/antiartificial/aurora/shell => ../shell

replace github.com/antiartificial/aurora/controller => ../controller

require (
	github.com/antiartificial/aurora/bus v0.0.0
	github.com/antiartificial/aurora/shell v0.0.0
)
