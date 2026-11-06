#version 100
//_DEFINES_
// Dual-Kawase upsample: 8 taps, the four edge midpoints x1 and the four
// diagonals x2, / 12. Drawn into a target twice the source size.
precision highp float;
uniform sampler2D tex;
uniform float alpha;
uniform vec2 halfpixel;   // 0.5 / source size
uniform float offset;
varying vec2 v_coords;
void main() {
    vec2 hp = halfpixel * offset;
    vec4 s = texture2D(tex, v_coords + vec2(-hp.x * 2.0, 0.0));
    s += texture2D(tex, v_coords + vec2(-hp.x, hp.y)) * 2.0;
    s += texture2D(tex, v_coords + vec2(0.0, hp.y * 2.0));
    s += texture2D(tex, v_coords + vec2(hp.x, hp.y)) * 2.0;
    s += texture2D(tex, v_coords + vec2(hp.x * 2.0, 0.0));
    s += texture2D(tex, v_coords + vec2(hp.x, -hp.y)) * 2.0;
    s += texture2D(tex, v_coords + vec2(0.0, -hp.y * 2.0));
    s += texture2D(tex, v_coords + vec2(-hp.x, -hp.y)) * 2.0;
    gl_FragColor = s / 12.0 * alpha;
}
