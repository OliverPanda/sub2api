-- 248_add_pelican_test_image_url.sql
-- Image-generation models may pass the connectivity probe while returning media
-- instead of an SVG document. Store one generated image so group results can be
-- rendered without persisting the full base64 response in raw_output.

ALTER TABLE pelican_test_results
    ADD COLUMN IF NOT EXISTS image_url TEXT NOT NULL DEFAULT '';
