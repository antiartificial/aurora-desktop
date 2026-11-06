#version 100
//_DEFINES_
// Glass layer: the blurred desktop, tinted, clipped to a rounded rectangle,
// with a highlight along the inside of the edge. Drawn over the window's
// rectangle; the window's own (translucent) pixels are drawn on top of it.
//
// Colour space (decision 0010, section 1): the backdrop arrives as the blur
// chain stores it (sqrt of linear light, or sRGB-encoded when `linear` is 0);
// everything this shader does to it (the luminance ceiling, the tint, the edge
// highlight) is done in LINEAR light; the result is encoded to sRGB on output
// because it is blended into the encoded scene. Tint and edge colours arrive
// already linear.
precision highp float;
uniform sampler2D tex;      // the blurred backdrop: half or quarter size, see texsize
uniform float alpha;
uniform vec4 rect;          // glass rectangle in OUTPUT pixels: x, y, w, h
uniform vec2 texsize;       // the OUTPUT size in pixels (not the texture's: the chain is downsampled)
uniform float radius;
uniform vec4 tint;          // LINEAR rgb, strength
uniform vec4 edge;          // LINEAR rgb, strength
uniform float edge_width;
uniform float lum_max;      // luminance ceiling Ymax (linear light); 0 = none (window glass)
uniform float linear;       // 1 = the texture holds sqrt(linear light)
varying vec2 v_coords;
float rbox(vec2 p, vec2 b, float r) {
    vec2 q = abs(p) - b + r;
    return min(max(q.x, q.y), 0.0) + length(max(q, 0.0)) - r;
}
vec3 decode(vec3 c) {
    c = clamp(c, 0.0, 1.0);
    return mix(pow((c + 0.055) / 1.055, vec3(2.4)), c / 12.92, step(c, vec3(0.04045)));
}
vec3 encode(vec3 c) {
    c = clamp(c, 0.0, 1.0);
    return mix(1.055 * pow(c, vec3(1.0 / 2.4)) - 0.055, 12.92 * c, step(c, vec3(0.0031308)));
}
void main() {
    vec2 px = v_coords * texsize - rect.xy;
    float d = rbox(px - rect.zw * 0.5, rect.zw * 0.5, radius);   // < 0 inside
    float cov = clamp(0.5 - d, 0.0, 1.0);
    vec3 col = texture2D(tex, v_coords).rgb;
    col = linear > 0.5 ? col * col : decode(col);
    // Luminance ceiling: a Reinhard roll-off of the backdrop's luminance, scaling the
    // linear triple so hue is kept. Y -> Y / (1 + Y / Ymax) asymptotes to Ymax.
    if (lum_max > 0.0) {
        float yb = dot(col, vec3(0.2126, 0.7152, 0.0722));
        col *= 1.0 / (1.0 + yb / lum_max);
    }
    col = mix(col, tint.rgb, tint.a);
    float band = 1.0 - smoothstep(0.0, max(edge_width, 0.001), -d);
    float lit = mix(1.0, 0.45, clamp(px.y / rect.w, 0.0, 1.0));
    col = mix(col, edge.rgb, band * edge.a * lit * step(d, 0.0));
    col = encode(col);
    gl_FragColor = vec4(col * cov, cov) * alpha;
}
