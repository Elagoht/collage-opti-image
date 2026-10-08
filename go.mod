// A collage plugin that rewrites declared-size images to resized copies it serves
// itself.
//
// It requires collage the way any consumer does, and reaches nothing the framework
// does not offer every plugin.
module github.com/Elagoht/collage-opti-image

go 1.26

require (
	github.com/Elagoht/collage v0.55.0
	github.com/HugoSmits86/nativewebp v1.3.0
	golang.org/x/image v0.24.0
)

retract v0.3.2 // tagged by mistake on the previous release's code; use v0.3.3 or later
