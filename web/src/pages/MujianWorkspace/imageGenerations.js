/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import { API } from '../../helpers';
import {
  getExternalImageContentBlob,
  isExternalImageContentUrl,
} from './imageContent';

export {
  imageContentExtension,
  isExternalImageContentUrl,
  saveImageBlob,
} from './imageContent';

export const imageReferenceAccept = '.png,.jpg,.jpeg,.webp';
export const maxImageReferences = 3;
export const maxImageReferenceBytes = 10 * 1024 * 1024;
export const maxImageReferenceTotalBytes = 14 * 1024 * 1024;

const supportedImageTypes = new Set(['image/png', 'image/jpeg', 'image/webp']);
const supportedImageExtensions = new Set(['png', 'jpg', 'jpeg', 'webp']);

const extensionOf = (name) => name.toLowerCase().split('.').pop();

const responseData = (response) => {
  if (!response.data?.success) {
    throw new Error(response.data?.message || '图片生成请求失败');
  }
  return response.data.data;
};

export const engineForImageModel = (modelId = '') => {
  const normalized = modelId.toLowerCase();
  if (normalized.startsWith('gpt-image')) return 'gpt';
  if (normalized.includes('nano-banana')) return 'nano';
  return '';
};

export const imageModelsByEngine = (models = []) => ({
  nano: models.filter((model) => engineForImageModel(model) === 'nano'),
  gpt: models.filter((model) => engineForImageModel(model) === 'gpt'),
});

export const createImageReference = (file) => {
  const extension = extensionOf(file.name);
  if (
    !supportedImageTypes.has(file.type) &&
    !supportedImageExtensions.has(extension)
  ) {
    throw new Error(`参考图 ${file.name} 仅支持 PNG、JPEG 或 WebP`);
  }
  if (!file.size) throw new Error(`参考图 ${file.name} 为空`);
  if (file.size > maxImageReferenceBytes) {
    throw new Error(`参考图 ${file.name} 超过 10MB`);
  }
  return {
    id: window.crypto.randomUUID(),
    file,
    name: file.name,
    size: file.size,
    previewUrl: URL.createObjectURL(file),
  };
};

export const releaseImageReference = (reference) => {
  if (reference?.previewUrl) URL.revokeObjectURL(reference.previewUrl);
};

export const createImageGeneration = async (
  projectId,
  sessionId,
  { prompt, engine, modelId, aspectRatio, references },
  signal,
) => {
  const form = new FormData();
  form.append('session_id', sessionId);
  form.append('prompt', prompt);
  form.append('engine', engine);
  form.append('model_id', modelId);
  form.append('aspect_ratio', aspectRatio);
  references.forEach((reference) => {
    form.append('reference_images', reference.file, reference.name);
  });
  return responseData(
    await API.post(
      `/api/mujian/projects/${projectId}/image-generations`,
      form,
      { signal, skipErrorHandler: true },
    ),
  );
};

export const getImageGeneration = async (
  projectId,
  sessionId,
  generationId,
  signal,
) =>
  responseData(
    await API.get(
      `/api/mujian/projects/${projectId}/image-generations/${generationId}`,
      { params: { session_id: sessionId }, signal, skipErrorHandler: true },
    ),
  );

export const getImageContentBlob = async (contentUrl, sessionId, signal) => {
  if (isExternalImageContentUrl(contentUrl)) {
    return getExternalImageContentBlob(contentUrl, signal);
  }
  return (
    await API.get(contentUrl, {
      params: { session_id: sessionId },
      signal,
      responseType: 'blob',
      skipErrorHandler: true,
    })
  ).data;
};

export const regenerateImageGeneration = async (
  projectId,
  sessionId,
  generationId,
  signal,
) =>
  responseData(
    await API.post(
      `/api/mujian/projects/${projectId}/image-generations/${generationId}/regenerate`,
      undefined,
      { params: { session_id: sessionId }, signal, skipErrorHandler: true },
    ),
  );
