require('dotenv').config({ path: '.env.production' });
const axios = require('axios');
const fs = require('fs');

const headers = {
	'Authorization': `Bearer ${process.env.STRAPI_API_TOKEN}`,
}
const baseUrl = process.env.STRAPI_API_URL

const fallbackLocale = 'en';

const fallBackGet = async (route, params) => {
	return await axios.get(baseUrl + '/' + route, {
		params: {...params, locale: fallbackLocale},
		headers
	})
}

// { locale?: string }
const get = async (route, params) => {
	const {data} = await axios.get(baseUrl + '/' + route, {
		params,
		headers
	})
		.catch(async (err) => {
			if (params.locale && params.locale !== fallbackLocale) return await fallBackGet(route)
			else {
				console.error('Error fetching data from Strapi:', err.message)
				return err
			}
		})
	return data
};

const ACCEPTED_LOCALES = [
	'en',
	'en-US', // browser default
	'ar',
	'zh', // zh chinese fallback
	'zh-Hans', // default zh
	'zh-Hant-TW',
	'ru',
	'fa',
	'es-419'
];

const getLocales = async () => {
	const locales = await get('i18n/locales', {});
	// create a zh locale for chinese fallback
	const zhHans = locales.find(locale => locale.code === 'zh-Hans');
	const lastId = locales[locales.length - 1].id;
	if (zhHans) locales.push({
		...zhHans,
		code: 'zh',
		id: lastId + 1
	});
	// create a redundant en-US locale for english browser behavior
	const en = locales.find(locale => locale.code === 'en');
	if (en) locales.push({
		...en,
		code: 'en-US',
		id: lastId + 2
	});
	return locales.filter(locale => ACCEPTED_LOCALES.includes(locale.code));
}

const runAsync = async () => {
	const locales = await getLocales();
	let translationsJson = {};

	for (const locale of locales) {
		console.log(`Translating items for ${locale.code}`);
		const translations = await get('unbounded-widget', { locale: locale.code });
		const {data} = translations;
		const {attributes} = data;
		// Add locale data to the main translationsJson object
		translationsJson[locale.code] = { translation: attributes };
	}

	// Write the combined JSON file
	fs.writeFileSync('src/translations.json', JSON.stringify(translationsJson, null, 2));

	console.log('Translations saved to src/translations.json');
}

runAsync();