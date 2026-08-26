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
import { LockKeyhole } from 'lucide-react';
import PageState from '../../components/common/ui/PageState';

const Forbidden = () => {
  const { t } = useTranslation();
  return (
    <PageState
      eyebrow='403 / FORBIDDEN'
      title={t('这个区域需要更高权限')}
      description={t(
        '您当前的账号无法访问此页面。如果这与预期不符，请联系管理员检查账号权限。',
      )}
      icon={<LockKeyhole />}
      actions={<Link to='/console'>{t('返回控制台')}</Link>}
    />
  );
};

export default Forbidden;
