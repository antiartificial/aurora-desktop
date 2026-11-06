#version 100
//_DEFINES_
// Switcher card: a window texture on a plane in 3D, rotated about Y and
// perspective-projected, by inverting the projection per pixel. Drawn as a
// full-screen quad, so v_coords are screen coordinates (0..1).
precision highp float;
uniform sampler2D tex;      // the window, live
uniform float alpha;
uniform vec2 screen;        // pixels
uniform vec4 pose;          // card centre x, y, z (z = depth from the eye), rotation about Y (rad)
uniform vec2 csize;         // card size = texture size, pixels
uniform float focal;        // focal length, pixels
uniform float shade;        // 1 = unlit
uniform float sel;          // > 0.5: draw the accent border
uniform vec3 accent;
varying vec2 v_coords;
void main() {
    vec2 p = v_coords * screen - screen * 0.5;
    vec3 dir = vec3(p / focal, 1.0);
    float ct = cos(pose.w), st = sin(pose.w);
    vec3 n = vec3(-st, 0.0, ct);
    float den = dot(n, dir);
    if (abs(den) < 0.0001) discard;
    float t = dot(n, pose.xyz) / den;
    if (t <= 0.0) discard;
    vec3 q = dir * t - pose.xyz;
    vec2 wp = vec2(dot(q, vec3(ct, 0.0, st)), q.y);     // card-plane pixels from centre
    vec2 half_ = csize * 0.5;
    vec2 dd = half_ - abs(wp);
    float spp = focal / t;                                // screen px per card px
    float cov = clamp(min(dd.x, dd.y) * spp + 0.5, 0.0, 1.0);
    if (cov <= 0.0) discard;
    vec2 uv = wp / csize + 0.5;
    float m = max(t / focal, 1.0) / 3.0;                  // minification: 3x3 box taps
    vec4 c = vec4(0.0);
    for (int i = -1; i <= 1; i++) {
        for (int j = -1; j <= 1; j++) {
            c += texture2D(tex, uv + vec2(float(i), float(j)) * m / csize);
        }
    }
    c = c / 9.0;
    c.rgb *= shade;
    vec4 o = c * cov;
    if (sel > 0.5) {
        float b = 1.0 - smoothstep(2.0, 4.0, min(dd.x, dd.y) * spp);
        o = mix(o, vec4(accent, 1.0) * cov, b);
    }
    gl_FragColor = o * alpha;
}
