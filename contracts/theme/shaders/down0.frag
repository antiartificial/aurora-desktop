#version 100
//_DEFINES_
// The first downsample of the shared backdrop prepass: full-size scene to half.
// Identical to down.frag except that its source is the ENCODED scene (the
// scene texture holds sRGB values, because client windows are blended into it
// in encoded space on purpose), so each tap goes through the exact IEC
// 61966-2-1 decode before the average, and the result is stored as
// sqrt(linear light). Only this pass pays for the pow(): three per tap.
precision highp float;
uniform sampler2D tex;
uniform float alpha;
uniform vec2 halfpixel;   // 0.5 / source size
uniform float offset;
uniform float linear;     // 1 = store sqrt(linear light); 0 = store the encoded value unchanged
varying vec2 v_coords;
vec3 decode(vec3 c) {
    c = clamp(c, 0.0, 1.0);
    return mix(pow((c + 0.055) / 1.055, vec3(2.4)), c / 12.92, step(c, vec3(0.04045)));
}
vec3 tap(vec2 uv) {
    vec3 c = texture2D(tex, uv).rgb;
    return linear > 0.5 ? decode(c) : c;
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
