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

export const agentAttachmentAccept =
  '.png,.jpg,.jpeg,.gif,.webp,.pdf,.txt,.md,.docx';
export const maxAgentAttachments = 4;
export const maxAgentAttachmentBytes = 10 * 1024 * 1024;
export const maxAgentAttachmentTotalBytes = 20 * 1024 * 1024;

const textExtensions = new Set(['txt', 'md']);
const imageMIMEByExtension = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  webp: 'image/webp',
};
const allowedExtensions = new Set([
  'png',
  'jpg',
  'jpeg',
  'gif',
  'webp',
  'pdf',
  'txt',
  'md',
  'docx',
]);

const extensionOf = (name) => name.toLowerCase().split('.').pop();

const readDataURL = (file) =>
  new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.onerror = () => reject(new Error(`无法读取附件 ${file.name}`));
    reader.readAsDataURL(file);
  });

export const readAgentAttachment = async (file) => {
  const extension = extensionOf(file.name);
  if (!allowedExtensions.has(extension)) {
    throw new Error('仅支持图片、PDF、Word、TXT 和 Markdown');
  }
  if (!file.size) throw new Error(`附件 ${file.name} 为空`);
  if (file.size > maxAgentAttachmentBytes) {
    throw new Error(`附件 ${file.name} 超过 10MB`);
  }
  if (textExtensions.has(extension) && file.size > 1024 * 1024) {
    throw new Error(`文本附件 ${file.name} 不能超过 1MB`);
  }

  const dataURL = await readDataURL(file);
  const separator = dataURL.indexOf(',');
  if (separator < 0) throw new Error(`附件 ${file.name} 数据无效`);
  const data = dataURL.slice(separator + 1);
  const mimeType = file.type || imageMIMEByExtension[extension] || '';
  return {
    id: window.crypto.randomUUID(),
    name: file.name,
    data,
    size: file.size,
    preview_url: imageMIMEByExtension[extension]
      ? `data:${mimeType};base64,${data}`
      : '',
  };
};

export const agentAttachmentPayload = (attachments) =>
  attachments.map(({ name, data }) => ({
    name,
    data,
  }));

export const agentAttachmentDisplayContent = (content, attachments) => {
  const lines = [content.trim() || '请分析附件内容。'];
  attachments.forEach((attachment) => lines.push(`📎 ${attachment.name}`));
  return lines.join('\n');
};

export const formatAttachmentSize = (bytes) => {
  if (bytes < 1024 * 1024) return `${Math.max(1, Math.round(bytes / 1024))}KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)}MB`;
};
