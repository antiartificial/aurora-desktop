#version 100
//_DEFINES_
// Desktop background: a slow, soft flow field. Domain-warped value noise
// moves between the theme's top and bottom colours; a warm accent bloom
// drifts through it. Hot-reloaded like the other shaders: copy this file into
// the theme directory as shaders/background.frag and edit it.
//
// Drawn over the whole screen by stretching a 1x1 texture, so v_coords is the
// 0..1 position on the screen with y = 0 at the top.
precision highp float;
uniform sampler2D tex;   // 1x1 placeholder; unused
uniform float alpha;
uniform float time;      // seconds * theme background.speed
uniform vec2 res;        // output size in device pixels
uniform vec3 top;        // sRGB, from the theme's Oklab tokens
uniform vec3 bottom;
uniform vec3 accent;
varying vec2 v_coords;

float hash(vec2 p) {
    p = fract(p * vec2(123.34, 456.21));
    p += dot(p, p + 45.32);
    return fract(p.x * p.y);
}
float vnoise(vec2 p) {
    vec2 i = floor(p), f = fract(p);
    vec2 u = f * f * (3.0 - 2.0 * f);
    return mix(mix(hash(i), hash(i + vec2(1.0, 0.0)), u.x),
               mix(hash(i + vec2(0.0, 1.0)), hash(i + vec2(1.0, 1.0)), u.x), u.y);
}
float fbm(vec2 p) {
    float a = 0.5, s = 0.0;
    for (int i = 0; i < 4; i++) { s += a * vnoise(p); p = p * 2.03 + 11.7; a *= 0.5; }
    return s;
}
void main() {
    float aspect = res.x / max(res.y, 1.0);
    vec2 p = vec2(v_coords.x * aspect, v_coords.y);
    float t = time * 0.035;
    vec2 q = vec2(fbm(p * 1.6 + vec2(t, -t * 0.7)), fbm(p * 1.6 + vec2(5.2 - t * 0.6, 1.3 + t)));
    float f = fbm(p * 1.3 + 2.4 * q + vec2(0.0, t * 0.5));
    float g = clamp(v_coords.y * 0.75 + (f - 0.5) * 0.9, 0.0, 1.0);
    vec3 col = mix(top, bottom, g);
    vec2 c = vec2(0.72 * aspect + 0.12 * sin(t * 3.1), 0.28 + 0.10 * cos(t * 2.3));
    float bloom = exp(-pow(length((p - c) * vec2(0.9, 1.4)), 2.0) * 3.2) * (0.55 + 0.45 * q.x);
    col = mix(col, accent, clamp(bloom * 0.55, 0.0, 1.0));
    col *= 1.0 - 0.28 * pow(length(v_coords - 0.5) * 1.35, 2.0);   // soft vignette
    col += (hash(gl_FragCoord.xy + fract(time)) - 0.5) / 255.0;    // dither: no banding on the gradient
    gl_FragColor = vec4(clamp(col, 0.0, 1.0), 1.0) * alpha;
}
