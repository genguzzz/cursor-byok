//! Resolves and persists images and blobs referenced by Cursor inputs.
use std::collections::HashSet;

use crate::{
    cursor::{protocol::proto::agent::v1 as pb, services::blob_sync::BlobSynchronizer},
    model::ContentPart,
    store::BlobId,
    Error, Result,
};

pub async fn parts(
    message: &pb::UserMessage,
    text: String,
    blobs: &BlobSynchronizer,
) -> Result<Vec<ContentPart>> {
    let mut parts = vec![ContentPart::Text {
        text: with_selected_images_section(text, message),
    }];
    if let Some(context) = &message.selected_context {
        for image in &context.selected_images {
            parts.push(ContentPart::Image {
                mime_type: image_mime_type(image)?,
                data: image_data(image, blobs).await?,
                path: image.path.trim().to_string(),
            });
        }
    }
    Ok(parts)
}

/// Keep the local path alongside the image bytes in the user message.
fn with_selected_images_section(text: String, message: &pb::UserMessage) -> String {
    let Some(section) = selected_images_section(message) else {
        return text;
    };
    if text.contains("<selected_images>") {
        return text;
    }
    if text.trim().is_empty() {
        section
    } else {
        format!("{}\n\n{section}", text.trim_end())
    }
}

fn selected_images_section(message: &pb::UserMessage) -> Option<String> {
    let context = message.selected_context.as_ref()?;
    let mut entries = Vec::new();
    let mut seen = HashSet::new();
    for image in &context.selected_images {
        let path = image.path.trim();
        if path.is_empty() || !seen.insert(path) {
            continue;
        }
        let mut attrs = format!("path=\"{}\"", escape_attr(path));
        let mime = image.mime_type.trim();
        if !mime.is_empty() {
            attrs.push_str(&format!(" mime_type=\"{}\"", escape_attr(mime)));
        }
        entries.push(format!("<image {attrs} />"));
    }
    if entries.is_empty() {
        return None;
    }
    Some(format!(
        "<selected_images>\n{}\n</selected_images>",
        entries.join("\n")
    ))
}

fn escape_attr(value: &str) -> String {
    value
        .replace('&', "&amp;")
        .replace('"', "&quot;")
        .replace('<', "&lt;")
}

fn image_mime_type(image: &pb::SelectedImage) -> Result<String> {
    let mime_type = image.mime_type.trim();
    if !mime_type.starts_with("image/") || mime_type.len() == "image/".len() {
        return Err(Error::Protocol(format!(
            "selected image has invalid MIME type: {}",
            image.mime_type
        )));
    }
    Ok(mime_type.into())
}

async fn image_data(image: &pb::SelectedImage, blobs: &BlobSynchronizer) -> Result<Vec<u8>> {
    use pb::selected_image::DataOrBlobId;

    let data = match image.data_or_blob_id.as_ref() {
        Some(DataOrBlobId::Data(data)) => data.clone(),
        Some(DataOrBlobId::BlobId(raw_id)) => {
            let id = BlobId::from_bytes(raw_id)?;
            blobs.get(&id).await?.ok_or_else(|| {
                Error::Protocol(format!(
                    "selected image Blob is missing: {}",
                    id.to_base64()
                ))
            })?
        }
        Some(DataOrBlobId::BlobIdWithData(value)) => {
            let id = BlobId::from_bytes(&value.blob_id)?;
            if value.data.is_empty() {
                blobs.get(&id).await?.ok_or_else(|| {
                    Error::Protocol(format!(
                        "selected image Blob is missing: {}",
                        id.to_base64()
                    ))
                })?
            } else {
                blobs.cache_received(&id, &value.data).await?;
                value.data.clone()
            }
        }
        None => {
            return Err(Error::Protocol(
                "selected image is missing data_or_blob_id".into(),
            ))
        }
    };
    if data.is_empty() {
        return Err(Error::Protocol("selected image data is empty".into()));
    }
    Ok(data)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn message_with_image(path: &str, mime_type: &str) -> pb::UserMessage {
        pb::UserMessage {
            selected_context: Some(pb::SelectedContext {
                selected_images: vec![pb::SelectedImage {
                    path: path.into(),
                    mime_type: mime_type.into(),
                    ..Default::default()
                }],
                ..Default::default()
            }),
            ..Default::default()
        }
    }

    #[test]
    fn selected_images_section_includes_path_without_read_hint() {
        let message = message_with_image("/tmp/a.png", "image/png");
        let section = selected_images_section(&message).unwrap();
        assert!(section.contains("<image path=\"/tmp/a.png\" mime_type=\"image/png\" />"));
        assert!(!section.contains("call the Read tool"));
    }

    #[test]
    fn selected_images_section_is_appended_once() {
        let message = message_with_image("/tmp/a.png", "image/png");
        let first = with_selected_images_section("look at this".into(), &message);
        let again = with_selected_images_section(first.clone(), &message);
        assert_eq!(first.matches("<selected_images>").count(), 1);
        assert_eq!(again, first);
        assert!(first.starts_with("look at this"));
    }

    #[test]
    fn selected_images_section_skips_empty_and_duplicate_paths() {
        let message = pb::UserMessage {
            selected_context: Some(pb::SelectedContext {
                selected_images: vec![
                    pb::SelectedImage {
                        path: String::new(),
                        mime_type: "image/png".into(),
                        ..Default::default()
                    },
                    pb::SelectedImage {
                        path: "/tmp/a.png".into(),
                        mime_type: "image/png".into(),
                        ..Default::default()
                    },
                    pb::SelectedImage {
                        path: "/tmp/a.png".into(),
                        mime_type: "image/jpeg".into(),
                        ..Default::default()
                    },
                ],
                ..Default::default()
            }),
            ..Default::default()
        };
        let section = selected_images_section(&message).unwrap();
        assert_eq!(section.matches("/tmp/a.png").count(), 1);
        assert!(!section.contains("image/jpeg"));
    }
}
