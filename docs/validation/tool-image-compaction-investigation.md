# Tool image compatibility: experimental

Keep this change outside the fork's `main` branch until the outstanding
compaction investigation is resolved and its regression case passes.

The patch uploads inline images in custom tool results through the existing
same-account attachment path. Native function-result images are moved into an
adjacent labeled user message because the upstream rejects images inside that
result array. Numbered markers preserve their positions relative to tool text;
call IDs, original image bytes, MIME types, detail and image order are retained.

Validation completed on the exact production incremental-stream baseline:

- Full Go race tests, `go vet` and Linux shared-library build passed.
- A normal-message image passed on the baseline; the same image in a custom
  result returned 422. The patched custom result passed and identified its color.
- A genuine generated function call with an image result passed with adaptation.
- HTTP, SSE and WebSocket image checks passed. Synthetic 320-message/21-tool-image
  compaction and continuation passed.

These checks do not reproduce the reported original compaction failure. Only
structural records survive: a closely matching failed request had 427 input
items and 21 PNGs in normal messages, with previous compaction, 36 encrypted
reasoning items and three agent messages. Two agent messages contained opaque
`encrypted_content` parts. The original text, image pixels and encrypted values
were not retained.

A separate 391-item reconstruction matched recorded image positions and encoded
lengths (3,398,514 bytes), included a 35,528-byte synthetic text part and a newly
generated valid compaction item. It omitted the 36 unavailable reasoning items
and substituted agent text. Compaction completed, but continuation returned an
upstream usage-policy 403 and subsequent controls returned 503. Further probes
were stopped. This is not an original-request replay or successful end-to-end
validation of that failure.

Remaining acceptance requires a reproducible representative failing context
covering the ordinary-message images, prior encrypted state and agent-message
content, followed by successful compaction and continuation. Preserve the
distinction between the verified tool-image defect and the unresolved original
compaction error when reporting results.
