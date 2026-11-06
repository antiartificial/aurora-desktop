#version 100
//_DEFINES_
// Glass layer: the blurred desktop, tinted, clipped to a rounded rectangle,
// with a highlight along the inside of the edge. Drawn over the window's
// rectangle; the window's own (translucent) pixels are drawn on top of it.
precision highp float;
uniform sampler2D tex;      // the blurred desktop, full screen
uniform float alpha;
uniform vec4 rect;          // window rectangle in texture pixels: x, y, w, h
uniform vec2 texsize;
uniform float radius;
uniform vec4 tint;          // sRGB rgb, strength
uniform vec4 edge;          // sRGB rgb, strength
uniform float edge_width;
varying vec2 v_coords;
float rbox(vec2 p, vec2 b, float r) {
    vec2 q = abs(p) - b + r;
    return min(max(q.x, q.y), 0.0) + length(max(q, 0.0)) - r;
}
void main() {
    vec2 px = v_coords * texsize - rect.xy;
    float d = rbox(px - rect.zw * 0.5, rect.zw * 0.5, radius);   // < 0 inside
    float cov = clamp(0.5 - d, 0.0, 1.0);
    vec3 col = texture2D(tex, v_coords).rgb;
    col = mix(col, tint.rgb, tint.a);
    float band = 1.0 - smoothstep(0.0, max(edge_width, 0.001), -d);
    float lit = mix(1.0, 0.45, clamp(px.y / rect.w, 0.0, 1.0));
    col = mix(col, edge.rgb, band * edge.a * lit * step(d, 0.0));
    gl_FragColor = vec4(col * cov, cov) * alpha;
}
