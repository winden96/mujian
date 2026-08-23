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

import { useEffect, useState } from 'react';
import { Button, Tag } from '@douyinfe/semi-ui';
import {
  AlertCircle,
  Check,
  Download,
  ExternalLink,
  Image as ImageIcon,
  LoaderCircle,
  RefreshCw,
} from 'lucide-react';
import {
  getImageContentBlob,
  imageContentExtension,
  isExternalImageContentUrl,
  saveImageBlob,
} from './imageGenerations';

const generationStatus = {
  queued: { label: '排队中', color: 'grey', icon: LoaderCircle },
  running: { label: '生成中', color: 'blue', icon: LoaderCircle },
  succeeded: { label: '已生成', color: 'green', icon: Check },
  failed: { label: '生成失败', color: 'red', icon: AlertCircle },
};

const ImageGenerationCard = ({
  generation,
  sessionId,
  regenerating,
  actionsDisabled,
  onRegenerate,
}) => {
  const [referencePreviews, setReferencePreviews] = useState(new Map());
  const [resultPreview, setResultPreview] = useState('');
  const [resultLoadFailed, setResultLoadFailed] = useState(false);
  const [resultDownloadExtension, setResultDownloadExtension] = useState('');
  const [resultDownloadPending, setResultDownloadPending] = useState(false);
  const [resultDownloadError, setResultDownloadError] = useState('');
  const [references] = useState(() => generation.references || []);
  const status = generationStatus[generation.status] || generationStatus.queued;
  const StatusIcon = status.icon;
  const isPending = ['queued', 'running'].includes(generation.status);

  useEffect(() => {
    const controller = new AbortController();
    const objectUrls = [];
    Promise.allSettled(
      references.map(async (reference) => {
        if (isExternalImageContentUrl(reference.content_url)) {
          return [reference.id, reference.content_url];
        }
        const blob = await getImageContentBlob(
          reference.content_url,
          sessionId,
          controller.signal,
        );
        if (controller.signal.aborted) return null;
        const objectUrl = URL.createObjectURL(blob);
        objectUrls.push(objectUrl);
        return [reference.id, objectUrl];
      }),
    ).then((results) => {
      if (controller.signal.aborted) return;
      setReferencePreviews(
        new Map(
          results
            .filter(
              (result) =>
                result.status === 'fulfilled' && result.value !== null,
            )
            .map((result) => result.value),
        ),
      );
    });
    return () => {
      controller.abort();
      objectUrls.forEach((objectUrl) => URL.revokeObjectURL(objectUrl));
    };
  }, [generation.id, references, sessionId]);

  useEffect(() => {
    setResultDownloadExtension('');
    setResultDownloadError('');
    if (generation.status !== 'succeeded' || !generation.result_url) {
      setResultPreview('');
      setResultLoadFailed(false);
      return;
    }
    if (isExternalImageContentUrl(generation.result_url)) {
      setResultPreview(generation.result_url);
      setResultDownloadExtension(imageContentExtension(generation.result_url));
      setResultLoadFailed(false);
      return;
    }
    const controller = new AbortController();
    let objectUrl = '';
    setResultPreview('');
    setResultLoadFailed(false);
    getImageContentBlob(generation.result_url, sessionId, controller.signal)
      .then((blob) => {
        if (controller.signal.aborted) return;
        objectUrl = URL.createObjectURL(blob);
        setResultPreview(objectUrl);
        setResultDownloadExtension(imageContentExtension('', blob.type));
      })
      .catch((error) => {
        if (error.code !== 'ERR_CANCELED' && !controller.signal.aborted) {
          setResultLoadFailed(true);
        }
      });
    return () => {
      controller.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [generation.result_url, generation.status, sessionId]);

  const downloadResult = async (event) => {
    event.preventDefault();
    if (resultDownloadPending) return;
    setResultDownloadPending(true);
    setResultDownloadError('');
    try {
      const blob = await getImageContentBlob(generation.result_url, sessionId);
      saveImageBlob(blob, generation.id, resultDownloadExtension);
    } catch {
      setResultDownloadError('原图下载失败，请稍后重试。');
    } finally {
      setResultDownloadPending(false);
    }
  };

  return (
    <article
      className={`mujian-generation-card is-${generation.status}`}
      aria-label={`图片生成任务：${status.label}`}
      aria-busy={isPending}
    >
      <header className='mujian-generation-card-header'>
        <div className='mujian-generation-card-heading'>
          <span className='mujian-generation-mark' aria-hidden='true'>
            <ImageIcon size={16} />
          </span>
          <div>
            <strong>
              {generation.engine === 'gpt' ? 'GPT 生成' : 'Nano 生成'}
            </strong>
            <small>
              {generation.model_id} · {generation.aspect_ratio}
            </small>
          </div>
        </div>
        <Tag color={status.color} prefixIcon={<StatusIcon size={12} />}>
          {status.label}
        </Tag>
      </header>

      <p className='mujian-generation-prompt'>{generation.prompt}</p>

      {references.length > 0 && (
        <div
          className='mujian-generation-references'
          aria-label={`${references.length} 张参考图`}
        >
          {references.map((reference) =>
            referencePreviews.has(reference.id) ? (
              <img
                key={reference.id}
                src={referencePreviews.get(reference.id)}
                alt={`参考图：${reference.name}`}
                title={reference.name}
                referrerPolicy='no-referrer'
                onError={() =>
                  setReferencePreviews((current) => {
                    const next = new Map(current);
                    next.delete(reference.id);
                    return next;
                  })
                }
              />
            ) : (
              <span
                key={reference.id}
                className='mujian-generation-reference-fallback'
                title={`参考图暂时无法加载：${reference.name}`}
                aria-label={`参考图暂时无法加载：${reference.name}`}
              >
                <ImageIcon size={15} aria-hidden='true' />
              </span>
            ),
          )}
          <span>{references.length} 张参考图</span>
        </div>
      )}

      {isPending && (
        <div className='mujian-generation-progress' role='status'>
          <span aria-hidden='true' />
          <span>
            {generation.status === 'queued'
              ? '任务已提交，正在等待生成资源…'
              : '正在合成画面，完成后会自动出现在这里…'}
          </span>
        </div>
      )}

      {generation.status === 'failed' && (
        <p className='mujian-generation-error' role='alert'>
          {generation.error || '图片生成失败，请稍后重试。'}
        </p>
      )}

      {generation.status === 'succeeded' && generation.result_url && (
        <figure className='mujian-generation-result'>
          {resultPreview && !resultLoadFailed ? (
            <img
              src={resultPreview}
              alt={generation.prompt}
              referrerPolicy='no-referrer'
              onError={() => setResultLoadFailed(true)}
            />
          ) : (
            <div role={resultLoadFailed ? 'alert' : 'status'}>
              {resultLoadFailed ? '生成结果暂时无法加载。' : '正在加载画面…'}
            </div>
          )}
        </figure>
      )}

      {(generation.status === 'failed' ||
        generation.status === 'succeeded') && (
        <footer className='mujian-generation-actions'>
          <Button
            size='small'
            theme='borderless'
            icon={<RefreshCw size={14} aria-hidden='true' />}
            loading={regenerating}
            disabled={regenerating || actionsDisabled}
            onClick={() => onRegenerate(generation.id)}
          >
            重新生成
          </Button>
          {generation.status === 'succeeded' &&
            resultPreview &&
            !resultLoadFailed && (
              <div className='mujian-generation-original-actions'>
                <a
                  className='mujian-generation-view'
                  href={resultPreview}
                  target='_blank'
                  rel='noreferrer'
                >
                  <ExternalLink size={14} aria-hidden='true' />
                  查看原图
                </a>
                <a
                  className='mujian-generation-download'
                  href={resultPreview}
                  aria-disabled={resultDownloadPending}
                  onClick={downloadResult}
                >
                  {resultDownloadPending ? (
                    <LoaderCircle size={14} aria-hidden='true' />
                  ) : (
                    <Download size={14} aria-hidden='true' />
                  )}
                  {resultDownloadPending ? '正在下载…' : '下载原图'}
                </a>
              </div>
            )}
        </footer>
      )}
      {resultDownloadError && (
        <p className='mujian-generation-error' role='alert'>
          {resultDownloadError}
        </p>
      )}
    </article>
  );
};

export default ImageGenerationCard;
