import React from 'react';
import { Carousel } from '@arco-design/web-react';
import useLocale from '@/utils/useLocale';
import locale from './locale';
import styles from './style/index.module.less';

export default function LoginBanner() {
  const t = useLocale(locale);
  const data = [
    {
      slogan: t['login.banner.slogan1'],
      subSlogan: t['login.banner.subSlogan1'],
      image: '/assets/login/organize.svg',
    },
    {
      slogan: t['login.banner.slogan2'],
      subSlogan: t['login.banner.subSlogan2'],
      image: '/assets/login/access.svg',
    },
    {
      slogan: t['login.banner.slogan3'],
      subSlogan: t['login.banner.subSlogan3'],
      image: '/assets/login/collaborate.svg',
    },
  ];
  return (
    <Carousel
      className={styles.carousel}
      animation="fade"
      autoPlay={{ interval: 3000, hoverToPause: false }}
      showArrow="never"
      indicatorType="dot"
      indicatorClassName={styles['carousel-indicator-static']}
    >
      {data.map((item, index) => (
        <div key={`${index}`}>
          <div className={styles['carousel-item']}>
            <div className={styles['carousel-title']}>{item.slogan}</div>
            <div className={styles['carousel-sub-title']}>{item.subSlogan}</div>
            <img
              alt=""
              aria-hidden="true"
              className={styles['carousel-image']}
              src={item.image}
            />
          </div>
        </div>
      ))}
    </Carousel>
  );
}
