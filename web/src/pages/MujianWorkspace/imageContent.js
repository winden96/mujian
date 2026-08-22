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

const supportedImageTypes = new Set(['image/png', 'image/jpeg', 'image/webp']);
const supportedImageExtensions = new Set(['png', 'jpg', 'jpeg', 'webp']);

const imageExtensionByMimeType = {
  'image/jpeg': 'jpg',
  'image/png': 'png',
  'image/webp': 'webp',
};

const extensionOf = (name) => name.toLowerCase().split('.').pop();

export const isExternalImageContentUrl = (contentUrl = '') =>
  /^https?:\/\//i.test(contentUrl);

export const imageContentExtension = (contentUrl = '', mimeType = '') => {
  if (imageExtensionByMimeType[mimeType]) {
    return imageExtensionByMimeType[mimeType];
  }
  try {
    const pathname = new URL(contentUrl, 'https://mujian.invalid').pathname;
    const extension = extensionOf(pathname);
    if (!supportedImageExtensions.has(extension)) return '';
    return extension === 'jpeg' ? 'jpg' : extension;
  } catch {
    return '';
  }
};

export const getExternalImageContentBlob = async (contentUrl, signal) => {
  const response = await fetch(contentUrl, {
    credentials: 'omit',
    referrerPolicy: 'no-referrer',
    signal,
  });
  if (!response.ok) {
    throw new Error(`图片下载失败（${response.status}）`);
  }
  const blob = await response.blob();
  if (!supportedImageTypes.has(blob.type.toLowerCase())) {
    throw new Error('图片下载返回了不支持的文件格式');
  }
  return blob;
};

export const saveImageBlob = (blob, generationId, preferredExtension = '') => {
  const extension =
    imageContentExtension('', blob.type) || preferredExtension || 'png';
  const objectUrl = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = objectUrl;
  link.download = `mujian-image-${generationId}.${extension}`;
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(objectUrl), 0);
};
