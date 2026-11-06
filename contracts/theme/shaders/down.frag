#version 100
//_DEFINES_
// Dual-Kawase downsample (Marius Bjorge, SIGGRAPH 2015): 5 taps, centre x4
// and four diagonal corners, / 8. Drawn into a target half the source size.
//
// The chain's targets hold sqrt(linear light) (gamma 2.0, decision 0010
// section 2.3) when `linear` is 1, so each tap is decoded with one multiply,
// averaged as light, and re-encoded with one sqrt. With `linear` 0 the chain
// holds sRGB-encoded values and averages them as they are (the pre-0010
// artefact, kept for A/B). The first downsample, which reads the encoded
// scene, is down0.frag.
precision highp float;
uniform sampler2D tex;
uniform float alpha;
uniform vec2 halfpixel;   // 0.5 / source size
uniform float offset;     // theme glass.blur_radius
uniform float linear;     // 1 = targets hold sqrt(linear light)
varying vec2 v_coords;
vec3 tap(vec2 uv) {
    vec3 c = texture2D(tex, uv).rgb;
    return linear > 0.5 ? c * c : c;
}
void main() {
    vec2 hp = halfpixel * offset;
    vec3 s = tap(v_coords) * 4.0;
    s += tap(v_coords - hp);
    s += tap(v_coords + hp);
    s += tap(v_coords + vec2(hp.x, -hp.y));
    s += tap(v_coords - vec2(hp.x, -hp.y));
    s /= 8.0;
    gl_FragColor = vec4(linear > 0.5 ? sqrt(s) : s, 1.0) * alpha;
}
