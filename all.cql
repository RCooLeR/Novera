CREATE INDEX IF NOT EXISTS FOR (o:Owner) ON (o.id);

CREATE INDEX IF NOT EXISTS FOR (o:Owner) ON (o.registered_at);

CREATE INDEX IF NOT EXISTS FOR (o:Owner) ON (o.registered_timestamp);

CREATE INDEX IF NOT EXISTS FOR (o:Owner) ON (o.registered_datestamp);

CREATE INDEX IF NOT EXISTS FOR (o:Owner) ON (o.email_valid);

CREATE INDEX IF NOT EXISTS FOR (o:Owner) ON (o.siblings);

CREATE INDEX IF NOT EXISTS FOR (o:Owner) ON (o.lastLogin);

CREATE INDEX IF NOT EXISTS FOR (o:Owner) ON (o.state);

CREATE INDEX IF NOT EXISTS FOR (o:Owner) ON (o.tmp_mail_updated_datestamp);

CREATE INDEX IF NOT EXISTS FOR (v:Vet) ON (v.id);

CREATE INDEX IF NOT EXISTS FOR (v:Vet) ON (v.email);

CREATE INDEX IF NOT EXISTS FOR (o:Vet) ON (o.registered_timestamp);

CREATE INDEX IF NOT EXISTS FOR (a:AnimalProfessional) ON (a.id);

CREATE INDEX IF NOT EXISTS FOR (o:AnimalProfessional) ON (o.registered_timestamp);

CREATE INDEX IF NOT EXISTS FOR (s:Survey) ON (s.id);

CREATE INDEX IF NOT EXISTS FOR (so:ShopifyOrder) ON (so.id);

CREATE INDEX IF NOT EXISTS FOR (so:ShopifyOrder) ON (so.financial_status);

CREATE INDEX IF NOT EXISTS FOR (so:ShopifyOrder) ON (so.created_at);

CREATE INDEX IF NOT EXISTS FOR (so:ShopifyOrder) ON (so.created_at_timestamp);

CREATE INDEX IF NOT EXISTS FOR (so:ShopifyOrder) ON (so.created_at_datestamp);

CREATE INDEX IF NOT EXISTS FOR (sop:Product) ON (sop.id);

CREATE INDEX IF NOT EXISTS FOR (pro:Promotion) ON (pro.id);

CREATE INDEX IF NOT EXISTS FOR (t:Transponder) ON (t.id);

CREATE INDEX IF NOT EXISTS FOR (t:Transponder) ON (t.productCode);

CREATE INDEX IF NOT EXISTS FOR (t:Transponder) ON (t.trackback_customer);

CREATE INDEX IF NOT EXISTS FOR (t:Transponder) ON (t.productCode, t.trackback_customer);

CREATE INDEX IF NOT EXISTS FOR (t:Transponder) ON (t.box, t.lot);

CREATE INDEX IF NOT EXISTS FOR (t:Transponder) ON (t.type);

CREATE INDEX IF NOT EXISTS FOR (t:Transponder) ON (t.prepaid);

CREATE INDEX IF NOT EXISTS FOR (t:Transponder) ON (t.client);

CREATE INDEX IF NOT EXISTS FOR (t:Transponder) ON (t.prepaid, t.type, t.client);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.id);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.transponder);

CREATE INDEX IF NOT EXISTS FOR (o:Pet) ON (o.registered_at);

CREATE INDEX IF NOT EXISTS FOR (o:Pet) ON (o.payment_datePaid_datestamp);

CREATE INDEX IF NOT EXISTS FOR (o:Pet) ON (o.transponderType);

CREATE INDEX IF NOT EXISTS FOR (o:Pet) ON (o.transponderClient);

CREATE INDEX IF NOT EXISTS FOR (o:Pet) ON (o.transponderPrepaid);

CREATE INDEX IF NOT EXISTS FOR (o:Pet) ON (o.payment_isPaid);

CREATE INDEX IF NOT EXISTS FOR (o:Pet) ON (o.reg_type);

CREATE INDEX IF NOT EXISTS FOR (o:Pet) ON (o.reg_type_transponder);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.registered_datestamp);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.trackback_sale_date_added);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.trackback_sale_id);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.trackback_customer);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.trackback_customer_manager);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.trackback_customer_state);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.registration_source_medium);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.registration_source_apid);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.registration_source_vetid);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.breedId);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.registered_datestamp, p.transponderType);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.isDatamars);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.specie);

CREATE INDEX IF NOT EXISTS FOR (p:Pet) ON (p.status);

CREATE INDEX IF NOT EXISTS FOR (p:Breed) ON (p.id);

CREATE INDEX IF NOT EXISTS FOR (p:Specie) ON (p.name);

CREATE INDEX IF NOT EXISTS FOR (p:Customer) ON (p.id);

CREATE INDEX IF NOT EXISTS FOR (p:Customer) ON (p.isShelter);

CREATE INDEX IF NOT EXISTS FOR (p:Customer) ON (p.isVet);

CREATE INDEX IF NOT EXISTS FOR (p:Customer) ON (p.manager);

CREATE INDEX IF NOT EXISTS FOR (p:Customer) ON (p.search_field);

CREATE INDEX IF NOT EXISTS FOR (p:TrackbackSale) ON (p.id);

CREATE INDEX IF NOT EXISTS FOR (p:TrackbackSale) ON (p.date_added);

CREATE INDEX IF NOT EXISTS FOR (p:TrackbackSale) ON (p.date_added_timestamp);

CREATE INDEX IF NOT EXISTS FOR (p:TrackbackSale) ON (p.date_added_datestamp);

CREATE INDEX IF NOT EXISTS FOR (p:TrackbackSale) ON (p.lot);

CREATE INDEX IF NOT EXISTS FOR (p:TrackbackSale) ON (p.box);

CREATE INDEX IF NOT EXISTS FOR (scan:WalletCardScan) ON (scan.code);

CREATE INDEX IF NOT EXISTS FOR (scan:WalletCardScan) ON (scan.id);

CREATE INDEX IF NOT EXISTS FOR (scan:WalletCardScan) ON (scan.productCode);

CREATE INDEX IF NOT EXISTS FOR (scan:WalletCardScan) ON (scan.customerId);

CREATE INDEX IF NOT EXISTS FOR (scan:WalletCardScan) ON (scan.created_datestamp);

CREATE INDEX IF NOT EXISTS FOR (scan:WalletCardScan) ON (scan.code_type);

CREATE INDEX IF NOT EXISTS FOR (scan:WalletCardScan) ON (scan.productCode, scan.created_datestamp);

CREATE INDEX IF NOT EXISTS FOR (scan:WalletCardScan) ON (scan.productCode, scan.customerId);

CREATE INDEX IF NOT EXISTS FOR (p:ClickEvent) ON (p.data_source, p.sparkpost_event_id);

CREATE INDEX IF NOT EXISTS FOR (p:ClickEvent) ON (p.data_source, p.action);

CREATE INDEX IF NOT EXISTS FOR (p:Country) ON (p.code);

CREATE INDEX IF NOT EXISTS FOR (p:State) ON (p.country, p.code);

CREATE INDEX IF NOT EXISTS FOR (p:City) ON (p.country, p.name);

CREATE INDEX IF NOT EXISTS FOR (p:Zip) ON (p.country, p.code);

CREATE INDEX IF NOT EXISTS FOR (p:Zip) ON (p.country, p.zcta);

CREATE INDEX IF NOT EXISTS FOR (p:RegistrationSource) ON (p.id);

CREATE INDEX IF NOT EXISTS FOR (p:Payment) ON (p.id);

CREATE INDEX IF NOT EXISTS FOR (p:Payment) ON (p.isPaid);
CREATE INDEX IF NOT EXISTS FOR (p:Payment) ON (p.reg_type);

CREATE INDEX IF NOT EXISTS FOR (p:Payment) ON (p.datePaid);

CREATE INDEX IF NOT EXISTS FOR (p:Payment) ON (p.datePaid_datestamp);

CREATE INDEX IF NOT EXISTS FOR (p:LostReport) ON (p.id);

CREATE INDEX IF NOT EXISTS FOR (p:FoundReport) ON (p.id);

CREATE INDEX IF NOT EXISTS FOR (p:LostReport) ON (p.resolution);

CREATE INDEX IF NOT EXISTS FOR (p:FoundReport) ON (p.resolution);

CREATE INDEX IF NOT EXISTS FOR (p:Transfer) ON (p.id);

CREATE INDEX IF NOT EXISTS FOR (p:Transfer) ON (p.datePaid);

CREATE INDEX IF NOT EXISTS FOR (p:Transfer) ON (p.datePaid_datestamp);

CREATE INDEX IF NOT EXISTS FOR (p:TransferSource) ON (p.id);

CREATE INDEX IF NOT EXISTS FOR (p:Dnc) ON (p.reason_id);

LOAD CSV WITH HEADERS FROM 'file:///zip.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
WITH row
  WHERE row.country_code IN ['US', 'CA']
MERGE (country:Country {code: row.country_code})

WITH row
  WHERE row.country_code IN ['US', 'CA'] AND row.state_id IS NOT NULL AND row.state_id <> ''
MATCH (country:Country {code: row.country_code})
MERGE (state:State {country: row.country_code, code: row.state_id})
SET state.name = row.state_name

WITH row
  WHERE row.country_code IN ['US', 'CA'] AND row.state_id IS NOT NULL AND row.state_id <> ''
MATCH (country:Country {code: row.country_code})
MATCH (state:State {country: row.country_code, code: row.state_id})
MERGE (country)-[:HAS_STATE]->(state)

WITH row
  WHERE row.country_code IN ['US', 'CA']
MERGE (zip:Zip {code: row.zip, country: row.country_code})

WITH row
  WHERE row.country_code IN ['US', 'CA'] AND row.latitude IS NOT NULL AND row.latitude <> '' AND row.
    longitude IS NOT NULL AND row.longitude <> ''
MERGE (zip:Zip {code: row.zip, country: row.country_code})
SET zip.latitude = toFloat(row.latitude), zip.longitude = toFloat(row.longitude)

WITH row
  WHERE row.country_code IN ['US', 'CA']
MATCH (country:Country {code: row.country_code})
MATCH (zip:Zip {code: row.zip, country: row.country_code})
MERGE (country)-[:HAS_ZIP]->(zip)

WITH row
  WHERE row.country_code IN ['US', 'CA'] AND row.state_id IS NOT NULL AND row.state_id <> ''
MATCH (state:State {country: row.country_code, code: row.state_id})
MATCH (zip:Zip {code: row.zip, country: row.country_code})
MERGE (state)-[:HAS_ZIP]->(zip)

} IN TRANSACTIONS OF 50 ROWS;

LOAD CSV WITH HEADERS FROM 'file:///zip_zcta.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (zip:Zip {code: row.zip, country: 'US'})
SET zip.zcta = row.zcta, zip.po_name = row.po_name

WITH row
  WHERE row.state IS NOT NULL AND row.state <> ''
MATCH (state:State {country: 'US', code: row.state})
MATCH (zip:Zip {code: row.zip, country: 'US'})
MERGE (state)-[:HAS_ZIP]->(zip)

} IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///zcta.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
WITH row
  WHERE row.zcta IS NOT NULL AND row.zcta <> ''
MERGE (zip:Zip {zcta: row.zcta, country: 'US'})
SET zip.population = toInteger(row.population),
zip.urban_population = toInteger(row.urban_population),
zip.rural_population = toInteger(row.rural_population),
zip.location_type = row.location_type,
zip.median_household_income = toInteger(replace(row.median_household_income, '$', ''))

} IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///0_breeds.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (p:Breed {id: row.id})
SET p.name = row.name
} IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///1_animalprofessionals.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
WITH row,
     CASE WHEN row.unsubscribe_default_insurance IS NULL OR row.unsubscribe_default_insurance = '' THEN false
       ELSE toBoolean(toInteger(row.unsubscribe_default_insurance))
       END AS unsubscribe_default_insurance
MERGE (ap:AnimalProfessional {id: tointeger(row.id)})
SET ap.email = row.email,
ap.role = row.role,
ap.name = apoc.text.replace(row.name, '\\s+', ' '),
ap.registered_at = datetime({datetime: datetime(replace(row.created, ' ', 'T') + '+00:00'),
                             timezone: 'America/New York'}),
ap.registered_timestamp = datetime({datetime: datetime(replace(row.created, ' ', 'T') + '+00:00'),
                                    timezone: 'America/New York'}).epochMillis,
ap.lastLogin = CASE WHEN row.lastLogin IS NULL OR row.lastLogin = '' THEN
null
  ELSE
datetime({datetime: datetime(replace(row.lastLogin, ' ', 'T') + '+00:00'), timezone: 'America/New York'})
  END,
ap.lastLogin_timestamp = CASE WHEN row.lastLogin IS NULL OR row.lastLogin = '' THEN
null
  ELSE
toInteger(datetime({datetime: datetime(replace(row.lastLogin, ' ', 'T') + '+00:00'), timezone: 'America/New York'}).
  epochMillis)
  END,
ap.country = row.addressCountry,
ap.state = row.stateid,
ap.phone = apoc.text.replace(replace(coalesce(row.addressPhonePrimary, ''), ' ', ''), '[^0-9+]', ''),
ap.unsubscribe_default_insurance = unsubscribe_default_insurance


WITH row
  WHERE row.addressCountry IS NOT NULL AND row.addressCountry <> ''
MATCH (ap:AnimalProfessional {id: tointeger(row.id)})
MERGE (country:Country {code: row.addressCountry})
MERGE (ap)-[:CITIZEN_OF]->(country)


WITH row
  WHERE row.stateid IS NOT NULL AND row.stateid <> ''
WITH row, split(row.stateid, '.') AS parts
  WHERE parts[1] IS NOT NULL AND parts[1] <> ''
MATCH (ap:AnimalProfessional {id: tointeger(row.id)})
MERGE (state:State {country: row.addressCountry, code: parts[1]})
MERGE (ap)-[r:LIVES_IN]->(state)
SET r.city = row.addressCity,
r.zip = row.addressPostcode

WITH row
  WHERE row.addressPostcode IS NOT NULL AND row.addressPostcode <> ''
MATCH (ap:AnimalProfessional {id: tointeger(row.id)})
MATCH (zip:Zip {code: row.addressPostcode, country: row.addressCountry})
MERGE (ap)-[:RESIDES_IN]->(zip)
}  IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///2_vets.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
WITH row,
     CASE WHEN row.unsubscribe_default_insurance IS NULL OR row.unsubscribe_default_insurance = '' THEN false
       ELSE toBoolean(toInteger(row.unsubscribe_default_insurance))
       END AS unsubscribe_default_insurance
MERGE (v:Vet {id: tointeger(row.id)})
SET v.email = row.email,
v.practiceName = apoc.text.replace(row.practiceName, '\\s+', ' '),
v.registered_at = datetime({datetime: datetime(replace(row.created, ' ', 'T') + '+00:00'),
                            timezone: 'America/New York'}),
v.registered_timestamp = datetime({datetime: datetime(replace(row.created, ' ', 'T') + '+00:00'),
                                   timezone: 'America/New York'}).epochMillis,
v.lastLogin = CASE WHEN row.lastLogin IS NULL OR row.lastLogin = '' THEN
null
  ELSE
datetime({datetime: datetime(replace(row.lastLogin, ' ', 'T') + '+00:00'), timezone: 'America/New York'})
  END,
v.lastLogin_timestamp = CASE WHEN row.lastLogin IS NULL OR row.lastLogin = '' THEN
null
  ELSE
toInteger(datetime({datetime: datetime(replace(row.lastLogin, ' ', 'T') + '+00:00'), timezone: 'America/New York'}).
  epochMillis)
  END,
v.country = row.addressCountry,
v.state = row.stateid,
v.phone = apoc.text.replace(replace(coalesce(row.addressPhonePrimary, ''), ' ', ''), '[^0-9+]', ''),
v.unsubscribe_default_insurance = unsubscribe_default_insurance


WITH row
  WHERE row.addressCountry IS NOT NULL AND row.addressCountry <> ''
MATCH (v:Vet {id: tointeger(row.id)})
MERGE (country:Country {code: row.addressCountry})
MERGE (v)-[:CITIZEN_OF]->(country)


WITH row
  WHERE row.stateid IS NOT NULL AND row.stateid <> ''
WITH row, split(row.stateid, '.') AS parts
  WHERE parts[1] IS NOT NULL AND parts[1] <> ''
MATCH (v:Vet {id: tointeger(row.id)})
MERGE (state:State {country: row.addressCountry, code: parts[1]})
MERGE (v)-[r:LIVES_IN]->(state)
SET r.city = row.addressCity,
r.zip = row.addressPostcode

WITH row
  WHERE row.addressPostcode IS NOT NULL AND row.addressPostcode <> ''
MATCH (v:Vet {id: tointeger(row.id)})
MERGE (zip:Zip {code: row.addressPostcode, country: row.addressCountry})
MERGE (v)-[:RESIDES_IN]->(zip)
}  IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///3_owners.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (o:Owner {id: toInteger(row.id)})
SET   o.firstName = apoc.text.replace(row.firstName, '\\s+', ' '),
o.lastName = apoc.text.replace(row.name, '\\s+', ' '),
o.email = row.email,
o.registered_at = datetime({datetime: datetime(replace(row.created, ' ', 'T') + '+00:00'),
                            timezone: 'America/New York'}),
o.registered_timestamp = datetime({datetime: datetime(replace(row.created, ' ', 'T') + '+00:00'),
                                   timezone: 'America/New York'}).epochMillis,
o.registered_datestamp = toInteger(apoc.temporal.format(datetime({datetime: datetime(replace(row.
  created, ' ', 'T') + '+00:00'),                                 timezone: 'America/New York'}), 'yyyyMMdd')),
o.lastLogin = CASE WHEN row.lastLogin IS NULL OR row.lastLogin = '' THEN
null
  ELSE
datetime({datetime: datetime(replace(row.lastLogin, ' ', 'T') + '+00:00'), timezone: 'America/New York'})
  END,
o.lastLogin_timestamp = CASE WHEN row.lastLogin IS NULL OR row.lastLogin = '' THEN
null
  ELSE
toInteger(datetime({datetime: datetime(replace(row.lastLogin, ' ', 'T') + '+00:00'), timezone: 'America/New York'}).
  epochMillis)
  END,
o.country = row.addressCountry,
o.state = row.stateid,
o.phone = apoc.text.replace(replace(coalesce(row.addressPhonePrimary, ''), ' ', ''), '[^0-9+]', ''),
o.contact1Email = row.contact1Email,
o.contact2Email = row.contact2Email,
o.contact1Phone = apoc.text.replace(replace(coalesce(row.contact1Phone, ''), ' ', ''), '[^0-9+]', ''),
o.contact2Phone = apoc.text.replace(replace(coalesce(row.contact2Phone, ''), ' ', ''), '[^0-9+]', ''),
o.unsubscribe = row.unsubscribe,
o.smsUnsubscribe = row.smsUnsubscribe,
o.unsubscribe_insurance = row.unsubscribe_insurance,
o.email_valid = CASE
  WHEN row.email =~ '^[^@]+@[^@]+\\.[^@]{2,}$' AND
NOT (row.email =~ '(@|@.*[.])te?mp([.]|$|d+$)') AND
NOT (row.email =~ '^[0-9]+@petlink.*$')
THEN true
  ELSE false
  END

WITH row
  WHERE row.addressCountry IS NOT NULL AND row.addressCountry <> ''
MATCH (o:Owner {id: tointeger(row.id)})
MERGE (country:Country {code: row.addressCountry})
MERGE (o)-[:CITIZEN_OF]->(country)


WITH row
  WHERE row.stateid IS NOT NULL AND row.stateid <> ''
WITH row, split(row.stateid, '.') AS parts
  WHERE parts[1] IS NOT NULL AND parts[1] <> ''
MATCH (o:Owner {id: tointeger(row.id)})
MERGE (state:State {country: row.addressCountry, code: parts[1]})
MERGE (o)-[r:LIVES_IN]->(state)
SET r.city = row.addressCity,
r.zip = row.addressPostcode

WITH row
  WHERE row.addressPostcode IS NOT NULL AND row.addressPostcode <> ''
MATCH (o:Owner {id: tointeger(row.id)})
MERGE (zip:Zip {code: row.addressPostcode, country: row.addressCountry})
MERGE (o)-[:RESIDES_IN]->(zip)
} IN TRANSACTIONS OF 50 ROWS;


MATCH (o:Owner)
CALL{
WITH o
MATCH (o)-[:OWNS]->(pet:Pet)
WITH o, count(pet) AS petCount
SET o.siblings = toInteger(petCount)
} IN TRANSACTIONS OF 100 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///4_gps_surveys.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (s:Survey {id: toInteger(row.survey_id)})
SET s.survey_type = row.survey_type,
s.updated_at = date(row.updated_at),
s.status = row.status,
s.has_gps = toBoolean(row.has_gps),
s.wear_time = row.wear_time,
s.gps_device = row.gps_device,
s.gps_device_group = row.gps_device_group,
s.device_type = row.device_type,
s.device_is_good = toBoolean(row.device_is_good),
s.positive_device_feedback = row.positive_device_feedback,
s.improve_feedback = row.improve_feedback,
s.would_buy = toBoolean(row.would_buy),
s.buy_reason = row.buy_reason,
s.wants_trial = toBoolean(row.wants_trial),
s.device_price = toFloat(row.device_price),
s.subscription_price = toFloat(row.subscription_price)
WITH row
MATCH (o:Owner {id: toInteger(row.owner_id)})
MATCH (s:Survey {id: toInteger(row.survey_id)})
MERGE (o)-[:HAS_SURVEY]->(s)
} IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///5_premium_surveys.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (s:Survey {id: toInteger(row.survey_id)})
SET s.survey_type = row.survey_type,
s.updated_at = date(row.updated_at),
s.status = row.status,
s.would_buy = row.would_buy,
s.veterinary247 = toInteger(row.veterinary247),
s.localNetwork = toInteger(row.localNetwork),
s.petInsurance = toInteger(row.petInsurance),
s.outletCoupon = toInteger(row.outletCoupon),
s.storeGift = toBoolean(row.storeGift),
s.revenue = toFloat(row.revenue),
s.otherServices = row.otherServices
WITH row
MATCH (o:Owner {id: toInteger(row.owner_id)})
MATCH (s:Survey {id: toInteger(row.survey_id)})
MERGE (o)-[:HAS_SURVEY]->(s)
} IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///6_shopify_orders.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (so:ShopifyOrder {id: toInteger(row.id)})
SET so.total_price = toFloat(row.total_price),
so.financial_status = row.financial_status,
so.status = row.status,
so.created_at = datetime(replace(row.created_at, ' ', 'T')),
so.created_at_timestamp = toInteger(datetime(replace(row.created_at, ' ', 'T')).epochMillis),
so.created_at_datestamp = toInteger(apoc.temporal.format(datetime({datetime: datetime(replace(row.
  created_at, ' ', 'T') + '-04:00'),                               timezone: 'America/New York'}), 'yyyyMMdd')),
so.lastUpdated = datetime(replace(row.lastUpdated, ' ', 'T'))

WITH row
MATCH (so:ShopifyOrder {id: toInteger(row.id)})
FOREACH (owner_id IN (CASE WHEN row.identified_owner_id IS NOT NULL THEN [toInteger(row.identified_owner_id)]
  ELSE []
  END) |
  MERGE (o:Owner {id: owner_id})
  MERGE (o)-[:PLACED_ORDER]->(so)
)
} IN TRANSACTIONS OF 50 ROWS;

LOAD CSV WITH HEADERS FROM 'file:///7_order_items.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
WITH row,
     CASE WHEN row.product_id IS NULL OR trim(row.product_id) = '' THEN row.variant_id
       ELSE row.product_id
       END AS productId
MERGE (p:Product {id: toInteger(productId)})
SET p.sku = row.sku,
p.title = row.title

WITH row
WITH row,
     CASE WHEN row.product_id IS NULL OR trim(row.product_id) = '' THEN row.variant_id
       ELSE row.product_id
       END AS productId
MATCH (so:ShopifyOrder {id: toInteger(row.order_id)})
MATCH (p:Product {id: toInteger(productId)})
MERGE (so)-[r:CONTAINS_PRODUCT]->(p)
SET r.variant_id = toInteger(row.variant_id),
r.quantity = toInteger(row.quantity),
r.name = row.name,
r.price = toFloat(row.price)


WITH row
WITH row,
     CASE WHEN row.product_id IS NULL OR trim(row.product_id) = '' THEN row.variant_id
       ELSE row.product_id
       END AS productId
MATCH (so:ShopifyOrder {id: toInteger(row.order_id)})
MATCH (p:Product {id: toInteger(productId)})
MATCH (o)-[:PLACED_ORDER]->(so)
MERGE (o)-[:PURCHASED_PRODUCT]->(p)

} IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///8_promotions.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (p:Promotion {id: toInteger(row.id)})
SET p.promoCode = row.promoCode,
p.amount = toFloat(row.amount),
p.startDate = datetime(replace(row.startDate, ' ', 'T') + '+00:00'),
p.endDate = datetime(replace(row.endDate, ' ', 'T') + '+00:00'),
p.maxCount = toInteger(row.maxCount)
} IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///9_transponders.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
WITH row,
     CASE WHEN row.prepaid IS NULL OR row.prepaid = '' THEN false
       ELSE toBoolean(toInteger(row.prepaid))
       END AS isPrepaid
MERGE (t:Transponder {id: row.id})
SET t.box = row.box,
t.lot = row.lot,
t.client = row.client,
t.prepaid = isPrepaid,
t.productCode = row.productCode,
t.clientLot = row.clientLot,
t.importDate = datetime(replace(row.importDate, ' ', 'T') + '+00:00'),
t.updated = datetime(replace(row.updated, ' ', 'T') + '+00:00'),
t.type = CASE
  WHEN row.client IN ['Crystal Tag Basic'] THEN 'Basic'
  WHEN row.client IN ['Crystal Tag BasicOne'] THEN 'BasicOne'
  WHEN row.client IN ['CrystalOneYear', 'CrystalOneYearPaid'] THEN 'OneYear'
  WHEN (NOT row.client IN ['Crystal Tag Basic', 'Crystal Tag BasicOne', 'CrystalOneYear',
  'CrystalOneYearPaid']) AND isPrepaid THEN 'Full'
  ELSE 'Unpaid'
  END
} IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///10_pets.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (p:Pet {id: toInteger(row.id)})
SET p.name = row.name,
p.transponder = row.transponder,
p.registered_at = datetime({datetime: datetime(replace(row.created, ' ', 'T') + '+00:00'),
                            timezone: 'America/New York'}),
p.registered_timestamp = toInteger(datetime({datetime: datetime(replace(row.created, ' ', 'T') + '+00:00'),
                                             timezone: 'America/New York'}).epochMillis),
p.registered_datestamp = toInteger(apoc.temporal.format(datetime({datetime: datetime(replace(row.
  created, ' ', 'T') + '+00:00'),                                 timezone: 'America/New York'}), 'yyyyMMdd')),
p.status = row.petStatus,
p.specie = CASE
  WHEN row.breedId STARTS WITH 'fe.' THEN 'Cat'
  WHEN row.breedId STARTS WITH 'ca.' THEN 'Dog'
  WHEN row.breedId STARTS WITH 'eq.' THEN 'Horse'
  ELSE 'Other'
  END,
p.breedId = row.breedId,
p.reg_type = row.reg_type,
p.reg_type_transponder = row.reg_type_transponder,
p.isDatamars = CASE WHEN row.transponder =~ '^981.*' THEN true
  ELSE false
  END,
p.hasVet = row.vetid,
p.hasAP = row.owningAnimalProfessional

WITH row
MATCH (p:Pet {id: toInteger(row.id)})
MERGE (o:Owner {id: toInteger(row.ownerid)})
MERGE (o)-[r:OWNS]->(p)

WITH row
MATCH (p:Pet {id: toInteger(row.id)})
OPTIONAL MATCH (v:Vet {id: toInteger(row.vetid)})
  WHERE row.vetid IS NOT NULL AND row.vetid <> ''
FOREACH (vet IN CASE WHEN v IS NOT NULL THEN [v]
  ELSE []
  END |
  MERGE (p)-[:ASSIGNED_TO]->(vet)
)

WITH row
MATCH (p:Pet {id: toInteger(row.id)})
OPTIONAL MATCH (v:Vet {email: row.vetEmail})
  WHERE (row.vetid IS NULL OR row.vetid = '') AND row.vetEmail IS NOT NULL AND row.vetEmail <> ''
FOREACH (vet IN CASE WHEN v IS NOT NULL THEN [v]
  ELSE []
  END |
  MERGE (p)-[:ASSIGNED_TO]->(vet)
)

WITH row
MATCH (p:Pet {id: toInteger(row.id)})
OPTIONAL MATCH (ap:AnimalProfessional {id: toInteger(row.owningAnimalProfessional)})
  WHERE row.owningAnimalProfessional IS NOT NULL
FOREACH (anp IN CASE WHEN ap IS NOT NULL THEN [ap]
  ELSE []
  END |
  MERGE (p)-[:ASSIGNED_TO]->(ap)
)

// keep row even if transponder is empty (don’t drop the row)
WITH row
MATCH (p:Pet {id: toInteger(row.id)})
FOREACH (_ IN CASE WHEN trim(coalesce(row.transponder, '')) <> '' THEN [1]
ELSE []
END |
MERGE (t:Transponder {id:row.transponder})
MERGE (p)- [:HAS_TRANSPONDER] - >(t)
)

WITH row
MATCH (p:Pet {id:toInteger(row.id)})
OPTIONAL MATCH (t:Transponder {id:row.transponder})
WITH row, p, t, trim(coalesce(p.reg_type_transponder, '')) AS rtt

// Rule A: if pet.reg_type_transponder is set -> force pet.transponderType from it
// Rule B: if pet.reg_type_transponder empty -> derive it from transponder type, and set transponderType = transponder type
SET
p.reg_type_transponder =
CASE
WHEN rtt = '' THEN
CASE coalesce(t.type, 'Unpaid')
WHEN 'Basic'    THEN 'basic'
WHEN 'BasicOne' THEN 'basic-one'
WHEN 'OneYear'  THEN 'oy'
WHEN 'Full'     THEN 'prepaid'
ELSE 'unpaid'
END
ELSE rtt
END,

p.transponderType =
CASE
WHEN rtt <> '' THEN
CASE toLower(rtt)
WHEN 'basic'     THEN 'Basic'
WHEN 'basic-one' THEN 'BasicOne'
WHEN 'oy'        THEN 'OneYear'
WHEN 'unpaid'    THEN 'Unpaid'
WHEN 'prepaid'   THEN 'Full'
WHEN 'full'      THEN 'Full'
ELSE coalesce(t.type, 'Unpaid')
END
ELSE coalesce(t.type, 'Unpaid')
END,

p.transponderPrepaid = coalesce(t.prepaid, false),
p.transponderClient = t.client


WITH row
MATCH (p:Pet {id:toInteger(row.id)})
FOREACH (breedId IN [row.breedId, row.breedId2, row.breedId3] |
FOREACH (id IN CASE WHEN coalesce(breedId, '') <> '' THEN [breedId]
ELSE []
END |
MERGE (b:Breed {id:id})
MERGE (p)- [:IS_OF_BREED] - >(b)
)
)

WITH row
WHERE  row.breedId IS NOT NULL AND row.breedId <> ''
MATCH (p:Pet {id:toInteger(row.id)})
MATCH (b:Breed {id:row.breedId})
MERGE (p)- [r:IS_OF_BREED] - >(b)
SET r.is_main = true

WITH row
MATCH (p:Pet {id:toInteger(row.id)})
MERGE (s:Specie {name:
CASE
WHEN row.breedId STARTS WITH 'fe.' THEN 'Cat'
WHEN row.breedId STARTS WITH 'ca.' THEN 'Dog'
WHEN row.breedId STARTS WITH 'eq.' THEN 'Horse'
ELSE 'Other'
END
})
MERGE (p)- [:IS_OF_SPECIE] - >(s)

}
IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///11_trackback_customers.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (c:Customer {id:toInteger(row.ClientID)})
SET c.practiceName = row.PracticeName,
c.practiceType = row.PracticeType,
c.state = row.State,
c.city = row.City,
c.email = row.Email,
c.ContactName = row.ContactName,
c.phoneNumber = apoc.text.replace(replace(coalesce(row.PhoneNumber, ''), ' ', ''), '[^0-9+]', ''),
c.isShelter = row.PracticeType IN ['S', 'R', 'SN', 'NP', 'B'],
c.isVet = row.PracticeType IN ['V'],
c.manager = CASE
WHEN row.PracticeType IN ['S', 'R', 'SN', 'NP', 'B'] AND toLower(row.State) IN [ 'me', 'us.me', 'maine', 'nh',
'us.nh', 'new hampshire', 'vt', 'us.vt', 'vermont', 'ma', 'us.ma', 'massachusetts', 'ri', 'us.ri', 'rhode island',
'ct', 'us.ct', 'connecticut', 'nj', 'us.nj', 'new jersey', 'md', 'us.md', 'maryland', 'ny', 'us.ny', 'new york', 'pa',
'us.pa', 'pennsylvania', 'wv', 'us.wv', 'west virginia', 'va', 'us.va', 'virginia', 'tn', 'us.tn', 'tennessee', 'nc',
'us.nc', 'north carolina', 'sc', 'us.sc', 'south carolina', 'ga', 'us.ga', 'georgia', 'ne', 'us.ne', 'nebraska', 'ks',
'us.ks', 'kansas', 'ok', 'us.ok', 'oklahoma', 'ar', 'us.ar', 'arkansas', 'la', 'us.la', 'louisiana', 'ms', 'us.ms',
'mississippi', 'ak', 'us.ak', 'alaska', 'al', 'us.al', 'alabama', 'dc', 'us.dc', 'district of columbia', 'de',
'us.de', 'delaware', 'ky', 'us.ky', 'kentucky' ]
THEN 'CherylAnn Fernandes'
WHEN row.PracticeType IN ['S', 'R', 'SN', 'NP', 'B'] AND toLower(row.State) IN ['wa', 'us.wa', 'washington', 'or',
'us.or', 'oregon', 'fl', 'us.fl', 'florida', 'nv', 'us.nv', 'nevada', 'ut', 'us.ut', 'utah', 'co', 'us.co',
'colorado', 'wv', 'us.wv', 'west virginia', 'nm', 'us.nm', 'new mexico', 'wi', 'us.wi', 'wisconsin', 'mi', 'us.mi',
'michigan', 'in', 'us.in', 'indiana', 'oh', 'us.oh', 'ohio', 'hi', 'us.hi', 'hawaii', 'ca', 'us.ca', 'california',
'tx', 'us.tx', 'texas', 'az', 'us.az', 'arizona', 'nd', 'us.nd', 'north dakota', 'sd', 'us.sd', 'south dakota', 'mn',
'us.mn', 'minnesota', 'ia', 'us.ia', 'iowa', 'mo', 'us.mo', 'missouri', 'il', 'us.il', 'illinois', 'wy', 'us.wy',
'wyoming', 'mt', 'us.mt', 'montana']
THEN 'Debby Collings'
ELSE 'Steve Wilcox'
END,
c.search_field = toLower(row.PracticeName) + ' ' + toLower(row.ContactName) + ' ' + toLower(row.
Email) + ' ' + toLower(row.City)
} IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///12_sales.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (ts:TrackbackSale {id:toInteger(row.SaleId)})
SET ts.date_added = datetime(row.DateAdded),
ts.date_added_timestamp = toInteger(datetime(row.DateAdded).epochMillis),
ts.date_added_datestamp = toInteger(row.datestamp),
ts.distributor = row.Distributor,
ts.box = row.BoxNumber,
ts.lot = row.LotNumber,
ts.items = toInteger(row.ItemsCount)

WITH row
MATCH (ts:TrackbackSale {id:toInteger(row.SaleId)})
MATCH (c:Customer {id:toInteger(row.ClientId)})
MERGE (c)- [:HAS_SALE] - >(ts)

WITH row
MATCH (t:Transponder {box:row.BoxNumber, lot:row.LotNumber})
MATCH (ts:TrackbackSale {id:toInteger(row.SaleId)})
MERGE (t)- [:PART_OF_SALE] - >(ts)
SET t.trackback_sale_id = toInteger(row.SaleId),
t.trackback_sale_date_added = datetime(row.DateAdded),
t.trackback_sale_date_added_datestamp = toInteger(row.datestamp),
t.trackback_sale_distributor = row.Distributor,
t.trackback_sale_box = row.BoxNumber,
t.trackback_sale_lot = row.LotNumber,
t.trackback_customer = toInteger(row.ClientId),
t.trackback_customer_practiceName = row.PracticeName,
t.trackback_customer_practiceType = row.PracticeType,
t.trackback_customer_state = row.State

} IN TRANSACTIONS OF 5 ROWS;

LOAD CSV WITH HEADERS FROM 'file:///12_sales.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MATCH (c:Customer {id:toInteger(row.ClientId)})
MATCH (t:Transponder {box:row.BoxNumber, lot:row.LotNumber})< - [:HAS_TRANSPONDER] -(pet:Pet)

WITH pet.id AS petId, row
MERGE (p:Pet {id:toInteger(petId)})
SET p.trackback_sale_id = toInteger(row.SaleId),
p.trackback_sale_date_added = datetime(row.DateAdded),
p.trackback_sale_date_added_datestamp = toInteger(row.datestamp),
p.trackback_sale_distributor = row.Distributor,
p.trackback_sale_box = row.BoxNumber,
p.trackback_sale_lot = row.LotNumber,
p.trackback_customer = toInteger(row.ClientId),
p.trackback_customer_practiceName = row.PracticeName,
p.trackback_customer_practiceType = row.PracticeType,
p.trackback_customer_state = row.State,
p.trackback_customer_email = row.Email,
p.trackback_customer_phoneNumber = apoc.text.replace(replace(coalesce(row.PhoneNumber, ''), ' ', ''), '[^0-9+]', ''),
p.trackback_customer_ContactName = row.ContactName,
p.trackback_customer_isShelter = row.PracticeType IN ['S', 'R', 'SN', 'NP', 'B'],
p.trackback_customer_isVet = row.PracticeType IN ['V'],
p.trackback_customer_manager = CASE
WHEN row.PracticeType IN ['S', 'R', 'SN', 'NP', 'B'] AND toLower(row.State) IN [ 'california', 'texas', 'maine',
'new hampshire', 'vermont', 'massachusetts', 'rhode island', 'connecticut', 'new jersey', 'delaware', 'maryland',
'dist of columbia', 'district of columbia', 'columbia', 'new york', 'virginia', 'tennessee', 'north carolina',
'south carolina', 'georgia', 'ca', 'tx', 'me', 'nh', 'vt', 'ma', 'ri', 'ct', 'nj', 'de', 'md', 'dc', 'ny', 'va', 'tn',
'nc', 'sc', 'ga', 'us.ca', 'us.tx', 'us.me', 'us.nh', 'us.vt', 'us.ma', 'us.ri', 'us.ct', 'us.nj', 'us.de', 'us.md',
'us.dc', 'us.ny', 'us.va', 'us.tn', 'us.nc', 'us.sc', 'us.ga' ]
THEN 'CherylAnn Fernandes'
WHEN row.PracticeType IN ['S', 'R', 'SN', 'NP', 'B'] AND toLower(row.State) IN [ 'washington', 'oregon', 'nevada',
'utah', 'colorado', 'arizona', 'new mexico', 'wisconsin', 'michigan', 'indiana', 'ohio', 'pennsylvania',
'west virginia', 'florida', 'hawaii', 'wa', 'or', 'nv', 'ut', 'co', 'az', 'nm', 'wi', 'mi', 'in', 'oh', 'pa', 'wv',
'fl', 'hi', 'us.wa', 'us.or', 'us.nv', 'us.ut', 'us.co', 'us.az', 'us.nm', 'us.wi', 'us.mi', 'us.in', 'us.oh',
'us.pa', 'us.wv', 'us.fl', 'us.hi']
THEN 'Debby Collings'
WHEN row.PracticeType IN ['S', 'R', 'SN', 'NP', 'B'] AND toLower(row.State) IN [ 'montana', 'north dakota',
'minnesota', 'idaho', 'wyoming', 'south dakota', 'nebraska', 'iowa', 'kansas', 'missouri', 'illinois', 'kentucky',
'oklahoma', 'arkansas', 'louisiana', 'mississippi', 'alabama', 'alaska', 'mt', 'nd', 'mn', 'id', 'wy', 'sd', 'ne',
'ia', 'ks', 'mo', 'il', 'ky', 'ok', 'ar', 'la', 'ms', 'al', 'ak', 'us.mt', 'us.nd', 'us.mn', 'us.id', 'us.wy',
'us.sd', 'us.ne', 'us.ia', 'us.ks', 'us.mo', 'us.il', 'us.ky', 'us.ok', 'us.ar', 'us.la', 'us.ms', 'us.al', 'us.ak' ]
THEN 'Julie Boudreau'
WHEN row.PracticeType IN ['V'] AND toLower(row.State) IN [ 'alaska', 'washington', 'oregon', 'montana', 'idaho',
'wyoming', 'california', 'nevada', 'utah', 'colorado', 'arizona', 'ak', 'wa', 'or', 'mt', 'id', 'wy', 'ca', 'nv',
'ut', 'co', 'az' ]
THEN 'Chris Stenroos'
ELSE 'Steve Wilcox'
END

} IN TRANSACTIONS OF 5 ROWS;

MATCH (pet:Pet)- [:HAS_TRANSPONDER] - >(t:Transponder)- [:PART_OF_SALE] - >(ts:TrackbackSale)
WITH count(DISTINCT pet) AS petCount, ts
SET ts.registrations = toInteger(petCount);


LOAD CSV WITH HEADERS FROM 'file:///13_clicks.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (o:Owner {id:toInteger(row.owner_id)})

WITH row
WHERE (row.sparkpost_event_id IS NULL OR row.sparkpost_event_id = '') AND  row.action IS NOT NULL AND row.action <> ''
MATCH (o:Owner {id:toInteger(row.owner_id)})
MERGE
(c:ClickEvent {data_source:row.data_source, action:row.action})
SET c.data_source = row.data_source,
c.action = row.action
MERGE (o)- [r:CLICKED] - >(c)
SET r.timestamp = datetime(replace(row.timestamp, ' ', 'T') + '-04:00'),
r.datestamp = toInteger(apoc.temporal.format(datetime({datetime:datetime(replace(row.
timestamp, ' ', 'T') + '-04:00'), timezone:'America/New York'}), 'yyyyMMdd')),
r.action = row.action

WITH row
WHERE row.sparkpost_event_id IS NOT NULL AND row.sparkpost_event_id <> ''
MATCH (o:Owner {id:toInteger(row.owner_id)})
MERGE
(c:ClickEvent {data_source:row.data_source, sparkpost_event_id:toInteger(row.sparkpost_event_id)})
SET c.data_source = row.data_source,
c.sparkpost_campaign = row.sparkpost_campaign,
c.sparkpost_template = row.sparkpost_template,
c.sparkpost_event_id = row.sparkpost_event_id,
c.action = row.action
MERGE (o)- [r:CLICKED] - >(c)
SET r.timestamp = datetime(replace(row.timestamp, ' ', 'T') + '-04:00'),
r.sparkpost_injection_time = datetime(replace(row.sparkpost_injection_time, ' ', 'T') + '-04:00'),
r.action = row.action

} IN TRANSACTIONS OF 1 ROWS;

LOAD CSV WITH HEADERS FROM 'file:///14_registration_sources.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (r:RegistrationSource {id:toInteger(row.id)})
SET r.medium = row.medium,
r.mediumId = row.mediumId,
r.email = row.email

WITH row
MATCH (r:RegistrationSource {id:toInteger(row.id)})
MERGE (t:Transponder {id:row.transponder})
MERGE (r)- [:OF_TRANSPONDER] - >(t)

WITH row
MATCH (r:RegistrationSource {id:toInteger(row.id)})
MATCH (t:Transponder {id:row.transponder})
MATCH (pet:Pet)- [:HAS_TRANSPONDER] - >(t:Transponder)
MERGE (p:Pet {id:pet.id})
SET p.registration_source_medium = row.medium,
p.registration_source_mediumId = row.mediumId,
p.registration_source_email = row.email,
p.registration_source_apid = CASE WHEN row.apid IS NOT NULL AND row.apid <> '' THEN toInteger(row.apid)
ELSE null
END,
p.registration_source_vetid = CASE WHEN row.vetid IS NOT NULL AND row.vetid <> '' THEN toInteger(row.vetid)
ELSE null
END

WITH row
WHERE row.apid IS NOT null AND row.apid <> ''
MATCH (r:RegistrationSource {id:toInteger(row.id)})
OPTIONAL MATCH (ap:AnimalProfessional {id:toInteger(row.apid)})
WHERE row.apid IS NOT NULL
FOREACH (anp IN CASE WHEN ap IS NOT NULL THEN [ap]
ELSE []
END |
MERGE (r)- [:REGISTERED_BY] - >(ap)
)

WITH row
WHERE row.vetid IS NOT NULL AND row.vetid <> ''
MATCH (r:RegistrationSource {id:toInteger(row.id)})
OPTIONAL MATCH (v:Vet {id:toInteger(row.vetid)})
WHERE row.vetid IS NOT NULL
FOREACH (vp IN CASE WHEN v IS NOT NULL THEN [v]
ELSE []
END |
MERGE (r)- [:REGISTERED_BY] - >(v)
)
}  IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///15_payments.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (p:Payment {id:toInteger(row.id)})
SET p.dateDue = CASE WHEN row.dateDue IS NOT NULL AND row.dateDue <> '' THEN
datetime({datetime:datetime(replace(row.dateDue, ' ', 'T') + '+00:00'), timezone:'America/New York'})
ELSE null
END,
p.datePaid = CASE WHEN row.datePaid IS NOT NULL AND row.datePaid <> '' THEN
datetime({datetime:datetime(replace(row.datePaid, ' ', 'T') + '+00:00'), timezone:'America/New York'})
ELSE null
END,
p.datePaid_datestamp = CASE WHEN row.datePaid IS NOT NULL AND row.datePaid <> '' THEN
toInteger(apoc.temporal.format(datetime({datetime:datetime(replace(row.datePaid, ' ', 'T') + '+00:00'),
timezone:'America/New York'}), 'yyyyMMdd'))
ELSE null
END,
p.dateCancelled = CASE WHEN row.dateCancelled IS NOT NULL AND row.dateCancelled <> '' THEN
datetime({datetime:datetime(replace(row.dateCancelled, ' ', 'T') + '+00:00'), timezone:'America/New York'})
ELSE null
END,
p.amount = toFloat(row.amount),
p.paymentRef = row.paymentRef,
p.updateAuthCode = row.updateAuthCode,
p.leadType = row.leadType,
p.reg_type = row.reg_type,
p.paidUser = row.paidUser,
p.paidRole = row.paidRole,
p.isPaid = CASE WHEN row.datePaid IS NOT NULL AND row.datePaid <> ''
AND (row.dateCancelled IS NULL OR row.dateCancelled = '')
AND NOT (coalesce(row.payment_method, '') IN ['cancelled', 'waived'])
THEN 1
ELSE 0
END

WITH row
WITH row
WHERE row.petid IS NOT NULL AND row.petid <> ''
MATCH (py:Payment {id:toInteger(row.id)})
MERGE (p:Pet {id:toInteger(row.petid)})
SET p.payment_id = toInteger(row.id),
p.payment_datePaid = CASE
WHEN row.datePaid IS NOT NULL AND row.datePaid <> '' THEN
datetime({datetime:datetime(replace(row.datePaid, ' ', 'T') + '+00:00'), timezone:'America/New York'})
ELSE null
END,
p.payment_datePaid_datestamp = CASE
WHEN row.datePaid IS NOT NULL AND row.datePaid <> '' THEN
toInteger(apoc.temporal.format(datetime({datetime:datetime(replace(row.datePaid, ' ', 'T') + '+00:00'), timezone:'America/New York'}), 'yyyyMMdd'))
ELSE null
END,
p.payment_dateCancelled = CASE
WHEN row.dateCancelled IS NOT NULL AND row.dateCancelled <> '' THEN
datetime({datetime:datetime(replace(row.dateCancelled, ' ', 'T') + '+00:00'), timezone:'America/New York'})
ELSE null
END,
p.payment_amount = toFloat(row.amount),
p.payment_leadType = row.leadType,
p.payment_paidUser = row.paidUser,
p.payment_paidRole = row.paidRole,
p.payment_reg_type = row.reg_type,
p.payment_isPaid = CASE
WHEN row.datePaid IS NOT NULL AND row.datePaid <> ''
AND (row.dateCancelled IS NULL OR row.dateCancelled = '')
AND NOT (coalesce(row.payment_method, '') IN ['cancelled', 'waived'])
THEN 1
ELSE 0
END

WITH row
WITH row
WHERE row.petid IS NOT NULL
MATCH (p:Payment {id:toInteger(row.id)})
MATCH (pet:Pet {id:toInteger(row.petid)})
MERGE (pet)- [r:HAS_PAYMENT] - >(p)

WITH row
WITH row
WHERE row.promoid IS NOT null AND row.promoid <> ''
MATCH (p:Payment {id:toInteger(row.id)})
MATCH (promo:Promotion {id:toInteger(row.promoid)})
MERGE (p)- [:USED_PROMO] - >(promo)

WITH row
WITH row
WHERE row.promoid IS NOT null AND row.promoid <> '' AND row.petid IS NOT NULL AND row.petid <> ''
MATCH (p:Payment {id:toInteger(row.id)})
MATCH (promo:Promotion {id:toInteger(row.promoid)})
MERGE (pet:Pet {id:toInteger(row.petid)})
SET pet.payment_promo = promo.promoCode

}  IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///16_lost.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (l:LostReport {id:toInteger(row.id)})
SET     l.resolution = row.resolutionId,
l.lostCountry = row.lostCountry,
l.lostPostcode = row.lostPostcode,
l.closedUser = row.closedUser,
l.circumstances = row.circumstances,
l.comment = row.comment,
l.dateAdded = datetime({datetime:datetime(replace(row.dateAdded, ' ', 'T') + '+00:00'), timezone:'America/New York'}),
l.dateClosed = CASE WHEN row.dateClosed IS NOT NULL AND row.dateClosed <> '' THEN
datetime({datetime:datetime(replace(row.dateClosed, ' ', 'T') + '+00:00'), timezone:'America/New York'})
ELSE null
END,
l.dateLost = CASE WHEN row.dateLost IS NOT NULL AND row.dateLost <> '' THEN
datetime({date: date(row.dateLost), timezone:'America/New York'})
ELSE null
END,
l.dateAdded_datestamp = toInteger(apoc.temporal.format(datetime({datetime:datetime(replace(row.
dateAdded, ' ', 'T') + '+00:00'), timezone:'America/New York'}), 'yyyyMMdd')),
l.dateClosed_datestamp = CASE WHEN row.dateClosed IS NOT NULL AND row.dateClosed <> '' THEN
toInteger(apoc.temporal.format(datetime({datetime:datetime(replace(row.dateClosed, ' ', 'T') + '+00:00'),
timezone:'America/New York'}), 'yyyyMMdd'))
ELSE null
END,
l.dateLost_datestamp = CASE WHEN row.dateLost IS NOT NULL AND row.dateLost <> '' THEN
toInteger(apoc.temporal.format(datetime({date: date(row.dateLost), timezone:'America/New York'}),  'yyyyMMdd'))
ELSE null
END

WITH row
MATCH (pet:Pet {id:toInteger(row.petId)})
MATCH (l:LostReport {id:toInteger(row.id)})
MERGE (pet)- [:REPORTED_LOST] - >(l)

WITH row
WHERE row.foundPetId IS NOT NULL AND row.foundPetId <> ''
MATCH (l:LostReport {id:toInteger(row.id)})
MERGE (f:FoundReport {id:toInteger(row.foundPetId)})
MERGE (l)- [:FOUND] - >(f)

WITH row
WHERE row.lostCountry IS NOT NULL AND row.lostCountry <> ''
MATCH (l:LostReport {id:toInteger(row.id)})
MATCH (country:Country {code:row.lostCountry})
MERGE (l)- [:LOST_IN_COUNTRY] - >(country)

WITH row
WHERE row.lostCountry IS NOT NULL AND row.lostCountry <> '' AND row.lostPostcode IS NOT null AND row.
lostPostcode <> ''
MATCH (l:LostReport {id:toInteger(row.id)})
MERGE (zip:Zip {code:row.lostPostcode, country:row.lostCountry})
MERGE (l)- [:LOST_IN_ZIP] - >(zip)

} IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///17_found.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (f:FoundReport {id:toInteger(row.id)})
SET     f.resolution = row.resolutionId,
f.foundCountry = row.addressCountry,
f.latitude = row.locationLat,
f.longitude = row.locationLong,
f.state = row.stateid,
f.finderType = row.finderTypeId,
f.finderEmail = row.finderEmail,
f.finderName = row.finderName,
f.finderOrgName = row.finderOrgName,
f.dateAdded = datetime({datetime:datetime(replace(row.dateAdded, ' ', 'T') + '+00:00'), timezone:'America/New York'}),
f.dateClosed = CASE WHEN row.dateClosed IS NOT NULL AND row.dateClosed <> '' THEN
datetime({datetime:datetime(replace(row.dateClosed, ' ', 'T') + '+00:00'), timezone:'America/New York'})
ELSE null
END,
f.dateFound = CASE WHEN row.dateFound IS NOT NULL AND row.dateFound <> '' THEN
datetime({date: date(row.dateFound), timezone:'America/New York'})
ELSE null
END,
f.dateAdded_datestamp = toInteger(apoc.temporal.format(datetime({datetime:datetime(replace(row.
dateAdded, ' ', 'T') + '+00:00'), timezone:'America/New York'}), 'yyyyMMdd')),
f.dateClosed_datestamp = CASE WHEN row.dateClosed IS NOT NULL AND row.dateClosed <> '' THEN
toInteger(apoc.temporal.format(datetime({datetime:datetime(replace(row.dateClosed, ' ', 'T') + '+00:00'),
timezone:'America/New York'}), 'yyyyMMdd'))
ELSE null
END,
f.dateFound_datestamp = CASE WHEN row.dateFound IS NOT NULL AND row.dateFound <> '' THEN
toInteger(apoc.temporal.format(datetime({date: date(row.dateFound), timezone:'America/New York'}), 'yyyyMMdd'))
ELSE null
END

WITH row
WHERE row.matchedPet IS NOT null AND row.matchedPet <> ''
MATCH (pet:Pet {id:toInteger(row.matchedPet)})
MATCH (f:FoundReport {id:toInteger(row.id)})
MERGE (pet)- [:REPORTED_FOUND] - >(f)

WITH row
WHERE row.matchedLostPet IS NOT NULL AND row.matchedLostPet <> ''
MATCH (l:LostReport {id:toInteger(row.matchedLostPet)})
MERGE (f:FoundReport {id:toInteger(row.id)})
MERGE (l)- [:FOUND] - >(f)

WITH row
WHERE row.addressCountry IS NOT NULL AND row.addressCountry <> ''
MATCH (l:FoundReport {id:toInteger(row.id)})
MATCH (country:Country {code:row.addressCountry})
MERGE (l)- [:FOUND_IN_COUNTRY] - >(country)

WITH row
WHERE row.addressCountry IS NOT NULL AND row.addressCountry <> '' AND row.stateid IS NOT null AND row.stateid <> ''
WITH row, split(row.stateid, '.') AS parts
WHERE parts[1] IS NOT NULL AND parts[1] <> ''
MATCH (l:FoundReport {id:toInteger(row.id)})
MERGE (state:State {country:row.addressCountry, code:parts[1]})
MERGE (l)- [:FOUND_IN_STATE] - >(state)

} IN TRANSACTIONS OF 50 ROWS;



LOAD CSV WITH HEADERS FROM 'file:///18_transfers.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (t:Transfer {id:toInteger(row.id)})
SET t.oldOwnerEmail = row.oldOwnerEmail,
t.newOwnerEmail = row.newOwnerEmail,
t.resolutionId = row.resolutionId,
t.dateClosed = CASE WHEN row.dateClosed IS NOT NULL AND row.dateClosed <> '' THEN
datetime({datetime:datetime(replace(row. dateClosed, ' ', 'T') + '+00:00'), timezone:'America/New York'})
ELSE null
END,
t.closedRole = row.closedRole,
t.closedUser = row.closedUser,
t.datePaid = CASE WHEN row.datePaid IS NOT null AND row.datePaid <> '' THEN
datetime({datetime:datetime(replace(row.datePaid, ' ', 'T') + '+00:00'), timezone:'America/New York'})
ELSE null
END,
t.datePaid_datestamp = CASE WHEN row.datePaid IS NOT NULL AND row.datePaid <> '' THEN
toInteger(apoc.temporal.format(datetime({datetime:datetime(replace(row.datePaid, ' ', 'T') + '+00:00'),
timezone:'America/New York'}), 'yyyyMMdd'))
ELSE null
END,
t.leadType = row.leadType,
t.leadId = row.leadId,
t.leadTime = row.leadTime,
t.amount = tofloat(row.amount),
t.isPaid = CASE WHEN row.datePaid IS NOT NULL AND row.datePaid <> ''
AND (row.dateCancelled IS NULL OR row.dateCancelled = '')
AND NOT (coalesce(row.payment_method, '') IN ['cancelled', 'waived'])
THEN 1
ELSE 0
END

WITH row
MATCH (t:Transfer {id:toInteger(row.id)})
MATCH (owner:Owner {id:toInteger(row.oldOwnerId)})
MERGE (t)- [:OLD_OWNER] - >(owner)

WITH row
MATCH (t:Transfer {id:toInteger(row.id)})
MATCH (owner:Owner {id:toInteger(row.newOwnerId)})
MERGE (t)- [:NEW_OWNER] - >(owner)

WITH row
MATCH (t:Transfer {id:toInteger(row.id)})
MATCH (pet:Pet {id:toInteger(row.petId)})
MERGE (t)- [:TRANSFERRED_PET] - >(pet)

WITH row
WHERE row.resolutionId = 'approved'
MATCH (pet:Pet {id:toInteger(row.petId)})
MATCH (owner:Owner {id:toInteger(row.oldOwnerId)})
MATCH (owner)- [r:OWNS] - >(pet)
DELETE r

WITH row
WHERE row.resolutionId = 'approved'
MERGE (p:Pet {id:toInteger(row.petId)})
SET p.transfer_datePaid = CASE WHEN row.datePaid IS NOT NULL AND row.datePaid <> '' THEN
datetime({datetime:datetime(replace(row.datePaid, ' ', 'T') + '+00:00'), timezone:'America/New York'})
ELSE null
END,
p.transfer_datePaid_datestamp = CASE WHEN row.datePaid IS NOT NULL AND row.datePaid <> '' THEN
toInteger(apoc.temporal.format(datetime({datetime:datetime(replace(row.datePaid, ' ', 'T') + '+00:00'),
timezone:'America/New York'}), 'yyyyMMdd'))
ELSE null
END,
p.transfer_dateClosed = CASE WHEN row.dateClosed IS NOT NULL AND row.dateClosed <> '' THEN
datetime({datetime:datetime(replace(row.dateClosed, ' ', 'T') + '+00:00'), timezone:'America/New York'})
ELSE null
END,
p.transfer_amount = toFloat(row.amount),
p.transfer_leadType = row.leadType,
p.transfer_closedUser = row.closedUser,
p.transfer_closedRole = row.closedRole,
p.transfer_isPaid = CASE WHEN row.datePaid IS NOT NULL AND row.datePaid <> ''
AND (row.dateCancelled IS NULL OR row.dateCancelled = '')
AND NOT (coalesce(row.payment_method, '') IN ['cancelled', 'waived'])
THEN 1
ELSE 0
END

WITH row
WHERE row.petTransponder IS NOT NULL AND row.petTransponder <> ''
MATCH (t:Transfer {id:toInteger(row.id)})
MATCH (transponder:Transponder {id:row.petTransponder})
MERGE (t)- [:TRANSFERRED_TRANSPONDER] - >(transponder)

WITH row
MATCH (t:Transfer {id:toInteger(row.id)})
MATCH (t:Transfer)- [:TRANSFERRED_PET] - >(pet:Pet)
MATCH (pet:Pet)- [:HAS_TRANSPONDER] - >(transponder:Transponder)
WHERE NOT (t)- [:TRANSFERRED_TRANSPONDER] - >(transponder:Transponder)
MERGE (t)- [:TRANSFERRED_TRANSPONDER] - >(transponder)

} IN TRANSACTIONS OF 50 ROWS;

MATCH (t:Transfer)- [:TRANSFERRED_PET] - >(pet:Pet)
MATCH (pet:Pet)- [:HAS_TRANSPONDER] - >(transponder:Transponder)
WHERE NOT (t)- [:TRANSFERRED_TRANSPONDER] - >(transponder:Transponder)
MERGE (t)- [:TRANSFERRED_TRANSPONDER] - >(transponder);


LOAD CSV WITH HEADERS FROM 'file:///19_transfer_sources.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (r:TransferSource {id:toInteger(row.id)})
SET r.medium = row.medium,
r.mediumId = row.mediumId,
r.email = row.email

WITH row
MATCH (r:TransferSource {id:toInteger(row.id)})
MERGE (t:Transponder {id:row.transponder})
MERGE (r)- [:OF_TRANSPONDER] - >(t)

WITH row
MATCH (r:TransferSource {id:toInteger(row.id)})
MATCH (t:Transponder {id:row.transponder})
MATCH (pet:Pet)- [:HAS_TRANSPONDER] - >(t:Transponder)
MERGE (p:Pet {id:pet.id})
SET p.transfer_source_medium = row.medium,
p.transfer_source_mediumId = row.mediumId,
p.transfer_source_email = row.email,
p.transfer_source_apid = CASE WHEN row.apid IS NOT NULL AND row.apid <> '' THEN toInteger(row.apid)
ELSE null
END,
p.transfer_source_vetid = CASE WHEN row.vetid IS NOT NULL AND row.vetid <> '' THEN toInteger(row.vetid)
ELSE null
END

WITH row
WHERE row.apid IS NOT null AND row.apid <> ''
MATCH (r:TransferSource {id:toInteger(row.id)})
OPTIONAL MATCH (ap:AnimalProfessional {id:toInteger(row.apid)})
WHERE row.apid IS NOT NULL
FOREACH (anp IN CASE WHEN ap IS NOT NULL THEN [ap]
ELSE []
END |
MERGE (r)- [:TRANSFER_BY] - >(ap)
)

WITH row
WHERE row.vetid IS NOT NULL AND row.vetid <> ''
MATCH (r:TransferSource {id:toInteger(row.id)})
OPTIONAL MATCH (v:Vet {id:toInteger(row.vetid)})
WHERE row.vetid IS NOT NULL
FOREACH (vp IN CASE WHEN v IS NOT NULL THEN [v]
ELSE []
END |
MERGE (r)- [:TRANSFER_BY] - >(v)
)
}  IN TRANSACTIONS OF 50 ROWS;

LOAD CSV WITH HEADERS FROM 'file:///20_email_replacements.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (o:Owner {id:toInteger(row.owner_id)})
SET o.tmp_mail_updated = date(row.updated_at),
o.tmp_mail_updated_timestamp = datetime(row.updated_at).epochMillis,
o.tmp_mail_updated_datestamp = toInteger(apoc.temporal.format(datetime({datetime:datetime(replace(row.
updated_at + ' 00:00:00', ' ', 'T') + '-04:00'), timezone:'America/New York'}), 'yyyyMMdd'))
} IN TRANSACTIONS OF 50 ROWS;


LOAD CSV WITH HEADERS FROM 'file:///21_dnc.csv' AS row
FIELDTERMINATOR ';'
CALL {
WITH row
MERGE (dnc:Dnc {reason_id:toInteger(row.reason), channel:'email'})
SET dnc.reason = CASE
WHEN 1 = toInteger(row.reason) THEN 'Unsubscribe'
WHEN 2 = toInteger(row.reason) THEN 'Bounce/Invalid email'
WHEN 3 = toInteger(row.reason) THEN 'Backend: unsubscribe, tmp email, no live pets'
END

WITH row
MERGE (o:Owner {id:toInteger(row.owner_id)})
SET o.dnc = true,
o.dnc_date_added = datetime({datetime:datetime(replace(row.date_added, ' ', 'T') + '+00:00'),
timezone:'America/New York'}),
o.dnc_date_added_datestamp = toInteger(apoc.temporal.format(datetime({datetime:datetime(replace(row.
date_added, ' ', 'T') + '+00:00'), timezone:'America/New York'}), 'yyyyMMdd')),
o.dnc_comments = row.comments,
o.dnc_reason = CASE
WHEN 1 = toInteger(row.reason) THEN 'Unsubscribe'
WHEN 2 = toInteger(row.reason) THEN 'Bounce/Invalid email'
WHEN 3 = toInteger(row.reason) THEN 'Backend: unsubscribe, tmp email, no live pets'
END,
o.dnc_last_sent_mail = row.last_sent_mail

WITH row
MATCH (o:Owner {id:toInteger(row.owner_id)})
MATCH (dnc:Dnc {reason_id:toInteger(row.reason), channel:'email'})
MERGE (o)- [r:ADDED_TO_DNC] - >(dnc)
SET r.date_added = datetime({datetime:datetime(replace(row.date_added, ' ', 'T') + '+00:00'),
timezone:'America/New York'}),
r.date_added_timestamp = toInteger(datetime({datetime:datetime(replace(row.date_added, ' ', 'T') + '+00:00'),
timezone:'America/New York'}).epochMillis),
r.date_added_datestamp = toInteger(apoc.temporal.format(datetime({datetime:datetime(replace(row.
date_added, ' ', 'T') + '+00:00'), timezone:'America/New York'}), 'yyyyMMdd')),
r.comments = row.comments,
r.reason = CASE
WHEN 1 = toInteger(row.reason) THEN 'Unsubscribe'
WHEN 2 = toInteger(row.reason) THEN 'Bounce/Invalid email'
WHEN 3 = toInteger(row.reason) THEN 'Backend: unsubscribe, tmp email, no live pets'
END,
r.last_sent_mail = row.last_sent_mail

} IN TRANSACTIONS OF 50 ROWS;
