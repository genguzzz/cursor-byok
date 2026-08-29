use std::io::Cursor;
use std::path::Path;

use image::codecs::jpeg::JpegEncoder;
use image::imageops::FilterType;
use image::{DynamicImage, GenericImageView, ImageReader};

const KIB: usize = 1024;

/// Aligned with the JPEG size CodeBuddy CLI produces when it compresses a
/// large screenshot (about 266 KiB), with headroom.
pub(super) const READ_IMAGE_BINARY_LIMIT: usize = 384 * KIB;

const MAX_SIDE: u32 = 2048;
const JPEG_QUALITY_LADDER: [u8; 7] = [80, 70, 60, 50, 40, 30, 20];
const SMALLER_SIDES: [u32; 4] = [1600, 1280, 960, 640];

/// Compress a Read tool image for replay, mirroring the fork's bridge logic:
/// image-like payloads (by path extension or magic bytes) get a 384 KiB budget
/// and are re-encoded as a JPEG when they exceed it.
pub(super) fn compress_read_image(path: &str, data: &[u8]) -> Vec<u8> {
    if !is_read_image_path(path) && !is_image_payload(data) {
        return data.to_vec();
    }
    compress_read_image_for_replay(data, READ_IMAGE_BINARY_LIMIT).unwrap_or_else(|| data.to_vec())
}

/// Port of `CompressReadImageForReplay`. Returns `None` when the payload cannot
/// be decoded and is over the limit; callers fall back to their normal binary
/// truncation path.
fn compress_read_image_for_replay(payload: &[u8], limit: usize) -> Option<Vec<u8>> {
    if payload.is_empty() {
        return None;
    }
    if is_image_payload(payload) && payload.len() <= limit {
        return Some(payload.to_vec());
    }
    let image = ImageReader::new(Cursor::new(payload))
        .with_guessed_format()
        .ok()?
        .decode()
        .ok()?;
    let scaled = scale_max_side(&image, MAX_SIDE);
    for quality in JPEG_QUALITY_LADDER {
        if let Some(encoded) = encode_jpeg(&scaled, quality) {
            if encoded.len() <= limit {
                return Some(encoded);
            }
        }
    }
    for side in SMALLER_SIDES {
        let smaller = scale_max_side(&image, side);
        if let Some(encoded) = encode_jpeg(&smaller, 40) {
            if encoded.len() <= limit {
                return Some(encoded);
            }
        }
    }
    None
}

pub(super) fn is_read_image_path(path: &str) -> bool {
    let extension = Path::new(path.trim())
        .extension()
        .and_then(|value| value.to_str())
        .unwrap_or_default()
        .to_ascii_lowercase();
    matches!(extension.as_str(), "jpg" | "jpeg" | "png" | "gif" | "webp")
}

pub(super) fn is_image_payload(payload: &[u8]) -> bool {
    if payload.len() >= 3 && payload[0] == 0xFF && payload[1] == 0xD8 && payload[2] == 0xFF {
        return true;
    }
    if payload.len() >= 8 && payload[0..4] == [0x89, 0x50, 0x4E, 0x47] {
        return true;
    }
    if payload.len() >= 6 && payload[0..3] == [0x47, 0x49, 0x46] {
        return true;
    }
    if payload.len() >= 12 && &payload[0..4] == b"RIFF" && &payload[8..12] == b"WEBP" {
        return true;
    }
    false
}

fn scale_max_side(image: &DynamicImage, max_side: u32) -> DynamicImage {
    if max_side == 0 {
        return image.clone();
    }
    let (width, height) = image.dimensions();
    if width <= max_side && height <= max_side {
        return image.clone();
    }
    image.resize(max_side, max_side, FilterType::Nearest)
}

fn encode_jpeg(image: &DynamicImage, quality: u8) -> Option<Vec<u8>> {
    let mut buffer = Cursor::new(Vec::new());
    let mut encoder = JpegEncoder::new_with_quality(&mut buffer, quality);
    encoder.encode_image(image).ok()?;
    Some(buffer.into_inner())
}

#[cfg(test)]
mod tests {
    use image::{ImageBuffer, Rgb};

    use super::*;

    fn large_compressible_png(side: u32) -> Vec<u8> {
        let image = ImageBuffer::from_fn(side, side, |x, y| {
            Rgb([(x & 0xFF) as u8, (y & 0xFF) as u8, ((x + y) & 0xFF) as u8])
        });
        let mut bytes = Cursor::new(Vec::new());
        DynamicImage::ImageRgb8(image)
            .write_to(&mut bytes, image::ImageFormat::Png)
            .unwrap();
        bytes.into_inner()
    }

    #[test]
    fn image_extensions_and_magic_bytes_are_detected() {
        for path in ["a.png", "B.JPEG", " /tmp/x.webp "] {
            assert!(is_read_image_path(path), "{path}");
        }
        assert!(!is_read_image_path("main.rs"));
        assert!(is_image_payload(&[0xFF, 0xD8, 0xFF, 0x00]));
        assert!(is_image_payload(&[
            0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A
        ]));
        assert!(is_image_payload(&[0x47, 0x49, 0x46, 0x38, 0x39, 0x61]));
        assert!(!is_image_payload(b"package main"));
    }

    #[test]
    fn small_images_pass_through_unchanged() {
        let png = large_compressible_png(4);
        assert!(png.len() <= READ_IMAGE_BINARY_LIMIT);
        assert_eq!(compress_read_image("/tmp/small.png", &png), png);
    }

    #[test]
    fn oversized_image_is_reencoded_as_a_jpeg_under_the_limit() {
        let png = large_compressible_png(1200);
        assert!(
            png.len() > READ_IMAGE_BINARY_LIMIT,
            "fixture should exceed limit, got {}",
            png.len()
        );
        let compressed = compress_read_image("/tmp/large.png", &png);
        assert!(compressed.len() <= READ_IMAGE_BINARY_LIMIT);
        assert!(is_image_payload(&compressed));
        assert_eq!(&compressed[..2], &[0xFF, 0xD8], "expected JPEG magic");
    }
}
