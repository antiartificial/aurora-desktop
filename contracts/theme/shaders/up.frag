#version 100
//_DEFINES_
// Dual-Kawase upsample: 8 taps, the four edge midpoints x1 and the four
// diagonals x2, / 12. Drawn into a target twice the source size. Decodes and
// re-encodes exactly as down.frag does.
precision highp float;
uniform sampler2D tex;
uniform float alpha;
uniform vec2 halfpixel;   // 0.5 / source size
uniform float offset;
uniform float linear;     // 1 = targets hold sqrt(linear light)
varying vec2 v_coords;
vec3 tap(vec2 uv) {
    vec3 c = texture2D(tex, uv).rgb;
    return linear > 0.5 ? c * c : c;
}
void main() {
    vec2 hp = halfpixel * offset;
    vec3 s = tap(v_coords + vec2(-hp.x * 2.0, 0.0));
    s += tap(v_coords + vec2(-hp.x, hp.y)) * 2.0;
    s += tap(v_coords + vec2(0.0, hp.y * 2.0));
    s += tap(v_coords + vec2(hp.x, hp.y)) * 2.0;
    s += tap(v_coords + vec2(hp.x * 2.0, 0.0));
    s += tap(v_coords + vec2(hp.x, -hp.y)) * 2.0;
    s += tap(v_coords + vec2(0.0, -hp.y * 2.0));
    s += tap(v_coords + vec2(-hp.x, -hp.y)) * 2.0;
    s /= 12.0;
    gl_FragColor = vec4(linear > 0.5 ? sqrt(s) : s, 1.0) * alpha;
}
