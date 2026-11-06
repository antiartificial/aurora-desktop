#version 100
//_DEFINES_
// Dual-Kawase downsample (Marius Bjorge, SIGGRAPH 2015): 5 taps, centre x4
// and four diagonal corners, / 8. Drawn into a target half the source size.
precision highp float;
uniform sampler2D tex;
uniform float alpha;
uniform vec2 halfpixel;   // 0.5 / source size
uniform float offset;     // theme glass.blur_radius
varying vec2 v_coords;
void main() {
    vec2 hp = halfpixel * offset;
    vec4 s = texture2D(tex, v_coords) * 4.0;
    s += texture2D(tex, v_coords - hp);
    s += texture2D(tex, v_coords + hp);
    s += texture2D(tex, v_coords + vec2(hp.x, -hp.y));
    s += texture2D(tex, v_coords - vec2(hp.x, -hp.y));
    gl_FragColor = s / 8.0 * alpha;
}
