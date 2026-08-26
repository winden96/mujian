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

import React from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { SearchX } from 'lucide-react';
import PageState from '../../components/common/ui/PageState';

const NotFound = () => {
  const { t } = useTranslation();
  return (
    <PageState
      eyebrow='404 / NOT FOUND'
      title={t('这个页面已经不在这里')}
      description={t('请检查地址是否正确，或回到首页继续使用幕间 AI。')}
      icon={<SearchX />}
      actions={<Link to='/'>{t('回到首页')}</Link>}
    />
  );
};

export default NotFound;
