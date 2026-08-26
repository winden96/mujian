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
import SwaggerUI from 'swagger-ui-react';
import 'swagger-ui-react/swagger-ui.css';
import './swagger-mujian.css';
import { omitRequestCredentials } from './docsRuntime';

const wrapAccessibleDeepLink = (OriginalDeepLink) => {
  const AccessibleDeepLink = ({ path, text, ...props }) => {
    if (path.includes('/')) {
      return <span>{text}</span>;
    }

    return <OriginalDeepLink path={path} text={text} {...props} />;
  };

  AccessibleDeepLink.displayName = 'AccessibleDeepLink';
  return AccessibleDeepLink;
};

const DisabledOnlineValidatorBadge = () => null;

const FilterContainer = ({ layoutSelectors, layoutActions, specSelectors }) => {
  if (specSelectors.loadingStatus() === 'loading') return null;
  const filter = layoutSelectors.currentFilter();
  const value = filter === true || filter === 'true' ? '' : filter || '';

  return (
    <div className='filter-container'>
      <div className='filter'>
        <input
          className='operation-filter-input'
          placeholder='按路径或标签筛选'
          type='search'
          value={value}
          onChange={(event) => layoutActions.updateFilter(event.target.value)}
        />
      </div>
    </div>
  );
};

const docsSwaggerPlugin = () => ({
  components: {
    onlineValidatorBadge: DisabledOnlineValidatorBadge,
    FilterContainer,
  },
  wrapComponents: {
    DeepLink: wrapAccessibleDeepLink,
  },
});

const SwaggerReference = ({ spec }) => (
  <SwaggerUI
    spec={spec}
    plugins={[docsSwaggerPlugin]}
    deepLinking
    filter
    displayRequestDuration
    requestSnippetsEnabled
    supportedSubmitMethods={['get', 'post']}
    persistAuthorization={false}
    requestInterceptor={omitRequestCredentials}
    docExpansion='list'
    defaultModelsExpandDepth={-1}
  />
);

export default SwaggerReference;
